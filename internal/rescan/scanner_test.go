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
