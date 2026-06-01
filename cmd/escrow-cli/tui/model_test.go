package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestModel_TabNavigationWraps(t *testing.T) {
	m := NewModel(nil) // offline (nil client) is fine for nav
	require.Equal(t, 0, m.tab)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, 1, m2.(Model).tab)
	// wrap: from last tab Tab returns to 0
	mm := m2.(Model)
	for i := 0; i < len(tabNames); i++ {
		next, _ := mm.Update(tea.KeyMsg{Type: tea.KeyTab})
		mm = next.(Model)
	}
	require.Equal(t, 1, mm.tab) // len wraps round to where we were +1
}

func TestModel_FilterCycling(t *testing.T) {
	m := NewModel(nil)
	require.Equal(t, "", m.eco)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	require.Equal(t, ecoCycle[1], m2.(Model).eco)

	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	require.Equal(t, activityCycle[1], m3.(Model).activity)
}

func TestModel_StreamEventPrependsAndCounts(t *testing.T) {
	m := NewModel(nil)
	m2, _ := m.Update(streamMsg{Event{Ecosystem: "npm", Action: "block", Kind: "scanned"}})
	mm := m2.(Model)
	require.Len(t, mm.events, 1)
	require.Equal(t, 1, mm.live.Blocked)
}

func TestModel_QuitKey(t *testing.T) {
	m := NewModel(nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd) // tea.Quit
}

func TestModel_PackagesExpandCollapse(t *testing.T) {
	m := NewModel(nil)
	m.tab = 3
	m.tree = []TreeEco{{Ecosystem: "npm", Packages: []TreePkg{
		{Name: "lodash", Versions: []TreeVer{{Version: "4.17.21", Action: "allow", Downloaded: true}}},
		{Name: "left-pad", Versions: []TreeVer{{Version: "1.3.0", Action: "allow", Downloaded: true}}},
	}}}
	require.Len(t, m.visiblePackages(), 2)
	require.Equal(t, 0, m.pkgCursor)

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // move to 2nd package
	require.Equal(t, 1, m2.(Model).pkgCursor)
	m3, _ := m2.(Model).Update(tea.KeyMsg{Type: tea.KeyDown}) // clamp at last
	require.Equal(t, 1, m3.(Model).pkgCursor)

	m4, _ := m3.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter}) // expand selected
	mm := m4.(Model)
	require.True(t, mm.pkgOpen["npm\x00left-pad"])

	mm.width, mm.height = 100, 30
	require.Contains(t, mm.View(), "1.3.0") // expanded version renders
}
