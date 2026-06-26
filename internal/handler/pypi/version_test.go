package pypi

import "testing"

func TestNormalizePyPI(t *testing.T) {
	cases := map[string]string{
		"Django":         "django",
		"zope.interface": "zope-interface",
		"Flask_Login":    "flask-login",
		"A.-_B":          "a-b",
		"requests":       "requests",
	}
	for in, want := range cases {
		if got := normalizePyPI(in); got != want {
			t.Errorf("normalizePyPI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPkgVersionFromFilename(t *testing.T) {
	cases := []struct {
		filename, wantName, wantVer string
	}{
		// Wheels (PEP 427) — reliable.
		{"requests-2.31.0-py3-none-any.whl", "requests", "2.31.0"},
		{"zope.interface-6.1-cp311-cp311-manylinux_2_17_x86_64.whl", "zope-interface", "6.1"},
		{"Flask_Login-0.6.3-py3-none-any.whl", "flask-login", "0.6.3"},
		// Modern (PEP 625) sdists use '_' in the name.
		{"zope_interface-6.1.tar.gz", "zope-interface", "6.1"},
		{"requests-2.31.0.tar.gz", "requests", "2.31.0"},
		{"foo-1.0.zip", "foo", "1.0"},
		// #67: legacy hyphenated sdist names — the version starts at the first
		// '-' followed by a digit, so the hyphenated name is preserved.
		{"django-allauth-0.50.0.tar.gz", "django-allauth", "0.50.0"},
		{"django-allauth-0.50.0.zip", "django-allauth", "0.50.0"},
		{"backports-tarfile-1.2.0.tar.gz", "backports-tarfile", "1.2.0"},
		// Non-artifacts / unparseable.
		{"requests.metadata", "", ""},
		{"noversion.whl", "", ""},
	}
	for _, c := range cases {
		name, ver := pkgVersionFromFilename(c.filename)
		if name != c.wantName || ver != c.wantVer {
			t.Errorf("pkgVersionFromFilename(%q) = (%q,%q), want (%q,%q)", c.filename, name, ver, c.wantName, c.wantVer)
		}
	}
}
