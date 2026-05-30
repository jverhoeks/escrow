package dashboard

import "testing"

func TestNamespaceFor(t *testing.T) {
	cases := []struct{ eco, name, wantNS, wantLeaf string }{
		{"npm", "@scope/pkg", "@scope", "pkg"},
		{"npm", "lodash", "", "lodash"},
		{"maven", "com.google.guava:guava", "com.google.guava", "guava"},
		{"go", "golang.org/x/net", "golang.org/x", "net"},
		{"nuget", "Microsoft.AspNetCore.Mvc", "Microsoft.AspNetCore", "Mvc"},
		{"pypi", "requests", "", "requests"},
		{"cargo", "serde", "", "serde"},
	}
	for _, c := range cases {
		ns, leaf := namespaceFor(c.eco, c.name)
		if ns != c.wantNS || leaf != c.wantLeaf {
			t.Errorf("namespaceFor(%q,%q) = (%q,%q), want (%q,%q)", c.eco, c.name, ns, leaf, c.wantNS, c.wantLeaf)
		}
	}
}
