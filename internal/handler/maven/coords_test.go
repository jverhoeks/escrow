package maven

import "testing"

func TestMavenCoordsFromPath(t *testing.T) {
	cases := []struct {
		path, wantName, wantVer string
	}{
		{"com/google/guava/guava/31.0/guava-31.0.jar", "com.google.guava:guava", "31.0"},
		{"org/example/lib/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-1.jar", "org.example.lib:lib", "1.0-SNAPSHOT"},
		{"org/example/app/2.0/app-2.0.war", "org.example:app", "2.0"},
		// Non-archive artifacts → no download event.
		{"com/google/guava/guava/31.0/guava-31.0.pom", "", ""},
		{"com/google/guava/guava/31.0/guava-31.0.jar.sha1", "", ""},
		{"com/google/guava/guava/31.0/guava-31.0.module", "", ""},
		// Minimal valid depth: group/artifact/version/file (4 segments).
		{"grp/art/1.0/art-1.0.jar", "grp:art", "1.0"},
		// Too shallow.
		{"1.0/foo-1.0.jar", "", ""},
		{"a.jar", "", ""},
	}
	for _, c := range cases {
		name, ver := mavenCoordsFromPath(c.path)
		if name != c.wantName || ver != c.wantVer {
			t.Errorf("mavenCoordsFromPath(%q) = (%q,%q), want (%q,%q)", c.path, name, ver, c.wantName, c.wantVer)
		}
	}
}
