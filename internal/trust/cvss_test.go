package trust

import "testing"

func TestCVSSBaseScore_V31(t *testing.T) {
	cases := []struct {
		vector string
		want   float64
		ok     bool
	}{
		// All-high, scope unchanged → 9.8 CRITICAL.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8, true},
		// Partial impact (A:L only), scope unchanged → 5.3 (rounding stress).
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L", 5.3, true},
		// Low score, scope unchanged → 2.0 LOW.
		{"CVSS:3.1/AV:N/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N", 2.0, true},
		// Scope changed + clamped to 10.0 CRITICAL.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H", 10.0, true},
		// CVSS 3.0 prefix is also accepted.
		{"CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8, true},
		// V4 / V2 / junk are not scoreable here.
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N", 0, false},
		{"AV:N/AC:L/Au:N/C:C/I:C/A:C", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := cvssBaseScore(c.vector)
		if ok != c.ok {
			t.Errorf("cvssBaseScore(%q) ok=%v, want %v", c.vector, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("cvssBaseScore(%q) = %.1f, want %.1f", c.vector, got, c.want)
		}
	}
}

func TestSeverityBandFromScore(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.0, ""}, {0.1, "LOW"}, {3.9, "LOW"},
		{4.0, "MEDIUM"}, {6.9, "MEDIUM"},
		{7.0, "HIGH"}, {8.9, "HIGH"},
		{9.0, "CRITICAL"}, {10.0, "CRITICAL"},
	}
	for _, c := range cases {
		if got := severityBandFromScore(c.score); got != c.want {
			t.Errorf("severityBandFromScore(%.1f) = %q, want %q", c.score, got, c.want)
		}
	}
}
