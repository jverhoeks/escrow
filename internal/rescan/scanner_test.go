package rescan_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/rescan"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/stretchr/testify/require"
)

func newOSV(t *testing.T, body string) *trust.OSVSignal {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	return trust.NewOSVSignal("HIGH", srv.Client(), cache.NewMemory(), srv.URL)
}

func TestRunOnce_NewVulnBlocksAndRecords(t *testing.T) {
	log := eventlog.New(100)
	// A downloaded version with no known vulns at scan time.
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	osv := newOSV(t, `{"vulns":[{"id":"GHSA-new","database_specific":{"severity":"HIGH"}}]}`)

	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: true})
	res := s.RunOnce(context.Background())

	require.Equal(t, 1, res.Scanned)
	require.Equal(t, 1, res.NewFindings)
	require.Equal(t, 1, res.Blocked)
	blocked, _ := bl.IsBlocked("npm", "lodash", "4.17.21")
	require.True(t, blocked)
	// A kind=rescan event was recorded carrying the vuln.
	var found bool
	for _, e := range log.Events("") {
		if e.Kind == eventlog.KindRescan && len(e.Vulns) == 1 && e.Vulns[0].ID == "GHSA-new" {
			found = true
		}
	}
	require.True(t, found)
}

// A non-GHSA advisory with only a LOW-scoring CVSS_V3 vector (no
// database_specific.severity) must NOT auto-block under min_severity=HIGH.
// Before #47 the rescanner over-blocked these (unknown severity → always
// counted); deriving the band from the CVSS vector fixes it.
func TestRunOnce_CVSSv3LowSeverity_NotBlockedUnderHigh(t *testing.T) {
	log := eventlog.New(100)
	log.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "somepkg@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	// CVSS:3.1 vector scoring 2.0 (LOW), no database_specific.severity.
	osv := newOSV(t, `{"vulns":[{"id":"PYSEC-2099-1","severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N"}]}]}`)
	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: true})

	res := s.RunOnce(context.Background())
	require.Equal(t, 0, res.NewFindings)
	require.Equal(t, 0, res.Blocked)
	blocked, _ := bl.IsBlocked("pypi", "somepkg", "1.0.0")
	require.False(t, blocked, "a LOW CVSS_V3 advisory must not be auto-blocked under min_severity=HIGH")
}

func TestRunOnce_DedupAcrossSweeps(t *testing.T) {
	log := eventlog.New(100)
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	osv := newOSV(t, `{"vulns":[{"id":"GHSA-new","database_specific":{"severity":"HIGH"}}]}`)
	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: true})

	require.Equal(t, 1, s.RunOnce(context.Background()).NewFindings)
	// Second sweep: same vuln is now baseline → no new finding.
	require.Equal(t, 0, s.RunOnce(context.Background()).NewFindings)
}

func TestRunOnce_OSVFailureIsSafe(t *testing.T) {
	log := eventlog.New(100)
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	osv := trust.NewOSVSignal("HIGH", srv.Client(), cache.NewMemory(), srv.URL)
	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: true})

	res := s.RunOnce(context.Background())
	require.Equal(t, 0, res.NewFindings)
	require.Equal(t, 0, res.Blocked)
	blocked, _ := bl.IsBlocked("npm", "lodash", "4.17.21")
	require.False(t, blocked) // never auto-block on a failed query
}

func TestScanner_SetConfig_AppliesLive(t *testing.T) {
	log := eventlog.New(100)
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	osv := newOSV(t, `{"vulns":[{"id":"GHSA-new","database_specific":{"severity":"HIGH"}}]}`)
	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: false})

	require.Equal(t, 1, s.RunOnce(context.Background()).NewFindings)
	blocked, _ := bl.IsBlocked("npm", "lodash", "4.17.21")
	require.False(t, blocked)

	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "left-pad@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	s.SetConfig(rescan.Config{MinSeverity: "HIGH", AutoBlock: true})
	s.RunOnce(context.Background())
	blocked2, _ := bl.IsBlocked("npm", "left-pad", "1.0.0")
	require.True(t, blocked2)
}
