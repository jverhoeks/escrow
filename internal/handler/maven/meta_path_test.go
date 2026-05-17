package maven_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/jverhoeks/escrow/internal/handler/maven"
)

func TestParseMavenMetaPath(t *testing.T) {
	tests := []struct {
		path         string
		wantGroup    string
		wantArtifact string
	}{
		{"com/example/mylib/maven-metadata.xml", "com.example", "mylib"},
		{"org/springframework/spring-core/maven-metadata.xml", "org.springframework", "spring-core"},
		{"com/example/deep/parent/artifact/maven-metadata.xml", "com.example.deep.parent", "artifact"},
		// Too shallow — can't determine groupId/artifactId
		{"mylib/maven-metadata.xml", "", ""},
		{"maven-metadata.xml", "", ""},
	}
	for _, tc := range tests {
		group, artifact := maven.ParseMavenMetaPath(tc.path)
		assert.Equal(t, tc.wantGroup, group, "groupID for %q", tc.path)
		assert.Equal(t, tc.wantArtifact, artifact, "artifactID for %q", tc.path)
	}
}
