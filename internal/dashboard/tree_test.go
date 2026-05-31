package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jverhoeks/escrow/internal/dlstats"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/trust"
)

// TestPackagesTree_ScannedVsDownloaded verifies that, for a single package
// version, the policy status is taken from the scanned (policy-evaluation)
// event while the Downloaded flag is OR-ed in from the downloaded event — and
// that the downloaded (action=allow) event does not clobber a real block status.
func TestPackagesTree_ScannedVsDownloaded(t *testing.T) {
	log := eventlog.New(50)
	// Recorded scanned-then-downloaded; Events() returns newest-first, so the
	// downloaded event is encountered before the scanned one.
	log.Record(eventlog.PackageEvent{
		Ecosystem: "npm", Package: "lodash@4.17.21", Action: "block", Signal: "osv",
		Reason: "known vulnerability", Kind: eventlog.KindScanned,
		Vulns: []trust.Vuln{
			{ID: "GHSA-aaaa", Severity: "CRITICAL"},
			{ID: "GHSA-bbbb", Severity: "HIGH"},
		},
	})
	log.Record(eventlog.PackageEvent{
		Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow",
		Reason: "artifact downloaded", Kind: eventlog.KindDownloaded,
	})

	d := &Dashboard{log: log}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/packages/tree?eco=npm", nil)
	d.handlePackagesTree(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out []TreeEcosystem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var tv *TreeVersion
	for i := range out {
		for j := range out[i].Packages {
			for k := range out[i].Packages[j].Versions {
				if out[i].Packages[j].Versions[k].Version == "4.17.21" {
					tv = &out[i].Packages[j].Versions[k]
				}
			}
		}
	}
	if tv == nil {
		t.Fatalf("version 4.17.21 not found in tree: %s", rec.Body.String())
	}
	if tv.Action != "block" {
		t.Errorf("Action = %q, want %q (status must come from the scanned event)", tv.Action, "block")
	}
	if tv.Signal != "osv" {
		t.Errorf("Signal = %q, want %q", tv.Signal, "osv")
	}
	if tv.CVECount != 2 {
		t.Errorf("CVECount = %d, want 2 (from scanned event vulns)", tv.CVECount)
	}
	if !tv.Downloaded {
		t.Errorf("Downloaded = false, want true (a downloaded event was recorded)")
	}
}

// TestPackages_DownloadedFlag verifies that a version with a downloaded
// (artifact-fetch) event is marked Downloaded==true on /api/packages, while a
// version with only scanned events stays Downloaded==false.
func TestPackages_DownloadedFlag(t *testing.T) {
	log := eventlog.New(50)
	// dl@1.0.0: scanned then downloaded → Downloaded should be true.
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "dl@1.0.0", Action: "allow", Kind: eventlog.KindScanned})
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "dl@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded, Reason: "artifact downloaded"})
	// scan@1.0.0: scanned only → Downloaded should be false.
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "scan@1.0.0", Action: "allow", Kind: eventlog.KindScanned})

	d := &Dashboard{log: log}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/packages?eco=npm", nil)
	d.handlePackages(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out []struct {
		Name       string `json:"name"`
		Downloaded bool   `json:"downloaded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, e := range out {
		got[e.Name] = e.Downloaded
	}
	if !got["dl"] {
		t.Errorf("dl Downloaded = false, want true (a downloaded event was recorded)")
	}
	if got["scan"] {
		t.Errorf("scan Downloaded = true, want false (only scanned events)")
	}
}

// TestTree_DownloadStats verifies that persistent download counts from the
// dlstats store are surfaced as download_count on the tree version rows.
func TestTree_DownloadStats(t *testing.T) {
	log := eventlog.New(50)
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	dl, _ := dlstats.New("")
	dl.Incr("npm", "lodash", "4.17.21")
	dl.Incr("npm", "lodash", "4.17.21")

	d := &Dashboard{log: log, dl: dl}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/packages/tree?eco=npm", nil)
	d.handlePackagesTree(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"download_count":2`) {
		t.Errorf("body missing download_count:2: %s", rec.Body.String())
	}
}

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
