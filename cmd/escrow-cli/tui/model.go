package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

var tabNames = []string{"Live", "CVEs", "Newly Vuln", "Packages", "Access", "Upstream"}
var ecoCycle = []string{"", "npm", "pypi", "cargo", "go", "composer", "nuget", "maven"}
var activityCycle = []string{"all", "downloaded", "scanned", "blocked"}

// Messages delivered to Update.
type streamMsg struct{ e Event }
type connMsg struct{ s string } // feed connection status (e.g. "reconnecting…")
type errMsg struct{ err error }
type statsMsg struct{ s Stats }
type eventsMsg struct{ events []Event }
type cvesMsg struct{ cves []CVE }
type newVulnMsg struct{ rows []NewVuln }
type treeMsg struct{ tree []TreeEco }
type accessMsg struct{ rows []AccessEntry }
type upstreamMsg struct{ rows []UpstreamEntry }

// Model is the Bubble Tea state for the TUI.
type Model struct {
	client   *Client // nil = offline
	offline  bool
	tab      int
	eco      string
	activity string
	width    int
	height   int
	status   string // status/error line

	live     Stats // running counts from the stream
	events   []Event
	cves     []CVE
	newvuln  []NewVuln
	tree     []TreeEco
	access   []AccessEntry
	upstream []UpstreamEntry
	scroll   int // per-tab scroll offset (reset on tab switch)
}

func NewModel(c *Client) Model {
	return Model{client: c, offline: c == nil, eco: "", activity: "all", status: ""}
}

// Init loads the first tab's data on startup (no-op when offline).
func (m Model) Init() tea.Cmd { return m.loadTab() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right":
			m.tab = (m.tab + 1) % len(tabNames)
			m.scroll = 0
			return m, m.loadTab()
		case "shift+tab", "left":
			m.tab = (m.tab - 1 + len(tabNames)) % len(tabNames)
			m.scroll = 0
			return m, m.loadTab()
		case "e":
			m.eco = next(ecoCycle, m.eco)
			return m, m.loadTab()
		case "a":
			m.activity = next(activityCycle, m.activity)
			return m, m.loadTab()
		case "r":
			return m, m.loadTab()
		case "down", "j":
			m.scroll++
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		}
		// number keys 1-6 jump to a tab
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '6' {
			m.tab = int(s[0] - '1')
			m.scroll = 0
			return m, m.loadTab()
		}
	case streamMsg:
		m.events = append([]Event{msg.e}, m.events...)
		if len(m.events) > 500 {
			m.events = m.events[:500]
		}
		switch msg.e.Action {
		case "block":
			m.live.Blocked++
		case "warn":
			m.live.Warned++
		case "allow":
			m.live.Allowed++
		}
	case connMsg:
		m.status = msg.s
	case statsMsg:
		m.live = msg.s
	case eventsMsg:
		m.events = msg.events
	case cvesMsg:
		m.cves = msg.cves
	case newVulnMsg:
		m.newvuln = msg.rows
	case treeMsg:
		m.tree = msg.tree
	case accessMsg:
		m.access = msg.rows
	case upstreamMsg:
		m.upstream = msg.rows
	case errMsg:
		m.status = "error: " + msg.err.Error()
	}
	return m, nil
}

func next(cycle []string, cur string) string {
	for i, v := range cycle {
		if v == cur {
			return cycle[(i+1)%len(cycle)]
		}
	}
	return cycle[0]
}

// loadTab returns a command that fetches the active tab's data (no-op offline).
func (m Model) loadTab() tea.Cmd {
	if m.client == nil {
		return nil
	}
	c := m.client
	switch m.tab {
	case 0:
		return func() tea.Msg {
			s, err := c.Stats()
			if err != nil {
				return errMsg{err}
			}
			return statsMsg{s}
		}
	case 1:
		return func() tea.Msg {
			v, err := c.CVEs()
			if err != nil {
				return errMsg{err}
			}
			return cvesMsg{v}
		}
	case 2:
		return func() tea.Msg {
			v, err := c.NewlyVulnerable()
			if err != nil {
				return errMsg{err}
			}
			return newVulnMsg{v}
		}
	case 3:
		return func() tea.Msg {
			t, err := c.PackagesTree()
			if err != nil {
				return errMsg{err}
			}
			return treeMsg{t}
		}
	case 4:
		return func() tea.Msg {
			a, err := c.AccessLog(200)
			if err != nil {
				return errMsg{err}
			}
			return accessMsg{a}
		}
	case 5:
		return func() tea.Msg {
			u, err := c.UpstreamLog(200)
			if err != nil {
				return errMsg{err}
			}
			return upstreamMsg{u}
		}
	}
	return nil
}
