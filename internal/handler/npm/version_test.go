package npm

import "testing"

func TestVersionFromTarball(t *testing.T) {
	cases := []struct {
		pkg, tarball, want string
	}{
		{"lodash", "lodash-4.17.21.tgz", "4.17.21"},
		{"@scope/pkg", "pkg-1.0.0.tgz", "1.0.0"},
		{"@babel/core", "core-7.24.0.tgz", "7.24.0"},
		{"foo", "foo-1.2.3-beta.1.tgz", "1.2.3-beta.1"},
		{"lodash", "lodash-4.17.21.json", ""}, // not a tarball
	}
	for _, c := range cases {
		if got := versionFromTarball(c.pkg, c.tarball); got != c.want {
			t.Errorf("versionFromTarball(%q,%q) = %q, want %q", c.pkg, c.tarball, got, c.want)
		}
	}
}
