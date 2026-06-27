package trust

import (
	"encoding/json"
	"testing"
)

// #101: decoding an arbitrary OSV response and turning it into a report must
// never panic, regardless of malformed/adversarial input.
func FuzzOSVToReport(f *testing.F) {
	f.Add([]byte(`{"vulns":[{"id":"GHSA-x","database_specific":{"severity":"HIGH"}}]}`))
	f.Add([]byte(`{"vulns":[{"id":"P","severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}]}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		var resp osvResponse
		if json.Unmarshal(data, &resp) != nil {
			return // not valid JSON for this shape; not under test
		}
		s := &OSVSignal{minSeverity: "HIGH"}
		_ = s.toReport(resp) // must not panic
	})
}
