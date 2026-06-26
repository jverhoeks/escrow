package trust

import (
	"math"
	"strings"
)

// cvssBaseScore computes the CVSS base score from a CVSS v3.0/v3.1 vector string
// (e.g. "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"), returning (score, true).
// It returns (0, false) for any other version (v2, v4) or a malformed vector —
// callers treat that as "unknown severity" and fail closed (include the vuln).
//
// The formula and metric weights follow the FIRST.org CVSS v3.1 specification
// (https://www.first.org/cvss/v3.1/specification-document §7). v3.0 shares the
// same base-metric formula, so the same computation applies.
func cvssBaseScore(vector string) (float64, bool) {
	if !strings.HasPrefix(vector, "CVSS:3.0/") && !strings.HasPrefix(vector, "CVSS:3.1/") {
		return 0, false
	}
	m := map[string]string{}
	for _, part := range strings.Split(vector, "/")[1:] {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}

	scopeChanged := m["S"] == "C"

	avW := map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}
	acW := map[string]float64{"L": 0.77, "H": 0.44}
	uiW := map[string]float64{"N": 0.85, "R": 0.62}
	ciaW := map[string]float64{"H": 0.56, "L": 0.22, "N": 0.0}
	// Privileges Required is scope-dependent.
	prW := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	if scopeChanged {
		prW = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}
	}

	av, ok1 := avW[m["AV"]]
	ac, ok2 := acW[m["AC"]]
	pr, ok3 := prW[m["PR"]]
	ui, ok4 := uiW[m["UI"]]
	c, ok5 := ciaW[m["C"]]
	i, ok6 := ciaW[m["I"]]
	a, ok7 := ciaW[m["A"]]
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
		return 0, false
	}

	iscBase := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if scopeChanged {
		impact = 7.52*(iscBase-0.029) - 3.25*math.Pow(iscBase-0.02, 15)
	} else {
		impact = 6.42 * iscBase
	}
	if impact <= 0 {
		return 0, true
	}
	exploitability := 8.22 * av * ac * pr * ui
	raw := impact + exploitability
	if scopeChanged {
		raw *= 1.08
	}
	return cvssRoundup(math.Min(raw, 10)), true
}

// cvssRoundup rounds up to one decimal place per the CVSS v3.1 spec (Appendix A):
// the smallest one-decimal value >= input, using integer arithmetic to avoid
// floating-point edge cases.
func cvssRoundup(input float64) float64 {
	intInput := int(math.Round(input * 100000))
	if intInput%10000 == 0 {
		return float64(intInput) / 100000.0
	}
	return (math.Floor(float64(intInput)/10000) + 1) / 10.0
}

// severityBandFromScore maps a CVSS base score to its qualitative band
// (CVSS v3.x §5). A 0.0 score ("None") returns "" — no severity.
func severityBandFromScore(score float64) string {
	switch {
	case score >= 9.0:
		return "CRITICAL"
	case score >= 7.0:
		return "HIGH"
	case score >= 4.0:
		return "MEDIUM"
	case score > 0.0:
		return "LOW"
	default:
		return ""
	}
}
