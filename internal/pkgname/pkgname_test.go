package pkgname

import "testing"

func TestSafe(t *testing.T) {
	ok := []string{
		"lodash", "@types/node", "@scope/pkg", "django-allauth",
		"github.com/foo/bar", "com/example/lib/1.0.0/lib-1.0.0.jar",
		"requests-2.31.0-py3-none-any.whl", "maven-metadata.xml", "zope.interface",
	}
	for _, s := range ok {
		if !Safe(s) {
			t.Errorf("Safe(%q) = false, want true", s)
		}
	}
	bad := []string{
		"", "..", ".", "a/../b", "../etc/passwd", "a//b", "/leading", "trailing/",
		"pkg?evil=1", "pkg#frag", "a b", "ctrl\x00", "back\\slash",
	}
	for _, s := range bad {
		if Safe(s) {
			t.Errorf("Safe(%q) = true, want false", s)
		}
	}
}
