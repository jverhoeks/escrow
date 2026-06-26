package pkgref

import "testing"

func TestSplit(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"lodash@4.17.21", "lodash", "4.17.21"},
		{"@scope/pkg@1.0.0", "@scope/pkg", "1.0.0"}, // scoped npm: split on the LAST '@'
		{"noversion", "noversion", ""},
		{"@scope/pkg", "@scope/pkg", ""}, // leading '@' is not a version delimiter
	}
	for _, c := range cases {
		n, v := Split(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("Split(%q) = (%q,%q), want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
	if got := Name("lodash@4.17.21"); got != "lodash" {
		t.Errorf("Name = %q", got)
	}
}
