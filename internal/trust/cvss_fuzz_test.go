package trust

import "testing"

// #101: cvssBaseScore must never panic and, when it reports a score, the score
// must be a valid CVSS band [0,10].
func FuzzCVSSBaseScore(f *testing.F) {
	f.Add("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
	f.Add("CVSS:3.0/AV:L/AC:H/PR:H/UI:R/S:C/C:L/I:N/A:N")
	f.Add("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N")
	f.Add("")
	f.Add("CVSS:3.1/garbage")
	f.Fuzz(func(t *testing.T, vector string) {
		score, ok := cvssBaseScore(vector)
		if ok && (score < 0 || score > 10) {
			t.Errorf("cvssBaseScore(%q) = %v, out of [0,10]", vector, score)
		}
	})
}
