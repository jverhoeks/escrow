package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestView_AllTabsRenderWithoutPanic exercises View() for every tab, both with
// data and empty, at a realistic and a zero viewport — the interactive TUI
// can't be driven without a TTY, so this guards against render panics.
func TestView_AllTabsRenderWithoutPanic(t *testing.T) {
	seed := Model{
		live:     Stats{Blocked: 1, Warned: 2, Allowed: 3},
		events:   []Event{{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "block", Kind: "scanned"}},
		cves:     []CVE{{ID: "GHSA-x", Severity: "HIGH", Ecosystem: "npm", Package: "lodash", Version: "4.17.11"}},
		newvuln:  []NewVuln{{Ecosystem: "npm", Package: "lodash", Version: "4.17.11", Vulns: []string{"GHSA-x"}, DownloadCount: 3}},
		tree:     []TreeEco{{Ecosystem: "npm"}},
		access:   []AccessEntry{{Host: "127.0.0.1", Method: "GET", Path: "/npm/lodash", Status: 200}},
		upstream: []UpstreamEntry{{Ecosystem: "npm", Method: "GET", URL: "https://registry.npmjs.org/lodash", Status: 200}},
		eco:      "", activity: "all",
	}
	for _, width := range []int{0, 120} {
		for tab := 0; tab < len(tabNames); tab++ {
			m := seed
			m.tab = tab
			m.width, m.height = width, 30
			out := m.View() // must not panic
			if width > 0 && strings.TrimSpace(out) == "" {
				t.Errorf("tab %d rendered empty at width %d", tab, width)
			}
		}
	}
	// empty model too
	(Model{width: 120, height: 30}).View()
	_ = tea.KeyMsg{}
}
