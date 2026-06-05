package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Styles (CVD-safe: blue/orange/yellow rather than green/red where it matters) ──
var (
	styTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))   // bright blue
	styDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))             // gray
	styOnline = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))   // blue
	styOff    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208"))  // orange
	styErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))             // orange
	styTabSel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")).Padding(0, 1)
	styTab    = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Padding(0, 1)
	styHead   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	styEco    = lipgloss.NewStyle().Foreground(lipgloss.Color("44")) // cyan
	styPkg    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styCursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")) // selected row

	// Decision badges — green allowed, amber warned, red blocked (CVD users still
	// have the glyph ✓/⚠/✕ to disambiguate, so color is a secondary cue).
	styAllow = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	styWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
	styBlock = lipgloss.NewStyle().Foreground(lipgloss.Color("203")) // red

	// Severity badges.
	sevCrit = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	sevHigh = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	sevMed  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sevLow  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

func statusBadge(action string) string {
	switch action {
	case "block":
		return styBlock.Render("✕ Blocked")
	case "warn":
		return styWarn.Render("⚠ Warned")
	default:
		return styAllow.Render("✓ Allowed")
	}
}

func sevBadge(sev string) string {
	switch strings.ToUpper(sev) {
	case "CRITICAL":
		return sevCrit.Render("CRITICAL")
	case "HIGH":
		return sevHigh.Render("HIGH")
	case "MEDIUM":
		return sevMed.Render("MEDIUM")
	case "LOW":
		return sevLow.Render("LOW")
	default:
		return sevLow.Render(orDash(sev))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// eventMatches mirrors escrow-cli live's liveMatch so online (SSE) and offline
// (file-tail) Live filtering agree.
func eventMatches(e Event, eco, activity string) bool {
	if eco != "" && e.Ecosystem != eco {
		return false
	}
	switch activity {
	case "", "all":
		return true
	case "downloaded":
		return e.Kind == "downloaded"
	case "scanned":
		return e.Kind != "downloaded" && e.Action != "block"
	case "blocked":
		return e.Action == "block"
	default:
		return true
	}
}

// truncate clamps s (which may contain ANSI styling) to at most w display
// columns, appending "…" when cut. It is ANSI-aware via lipgloss.Width.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// s may have styling; truncate by visible runes conservatively. For our
	// rows the styling is per-cell, so a plain rune-trim with an ellipsis keeps
	// it readable even if it occasionally trims a trailing reset (lipgloss
	// closes styles per Render call).
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func (m Model) View() string {
	// Before the first WindowSizeMsg width/height are 0; pick sane defaults so
	// we never slice/truncate against a zero viewport.
	w := m.width
	if w <= 0 {
		w = 100
	}
	h := m.height
	if h <= 0 {
		h = 24
	}

	var b strings.Builder

	// ── Top bar ──
	conn := styOnline.Render("● online")
	if m.offline {
		conn = styOff.Render("○ offline")
	}
	filters := styDim.Render(fmt.Sprintf("eco=%s activity=%s", orAllFilter(m.eco), m.activity))
	top := fmt.Sprintf("%s · %s · %s", styTitle.Render("ESCROW tui"), conn, filters)
	b.WriteString(truncate(top, w))
	b.WriteString("\n")

	// Status / error line.
	status := m.status
	if status != "" {
		b.WriteString(truncate(styErr.Render(status), w))
	}
	b.WriteString("\n")

	// ── Tab strip ──
	var tabs []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if i == m.tab {
			tabs = append(tabs, styTabSel.Render(label))
		} else {
			tabs = append(tabs, styTab.Render(label))
		}
	}
	b.WriteString(truncate(strings.Join(tabs, " "), w))
	b.WriteString("\n\n")

	// ── Body ── (reserve top bar(3) + tab strip(2) + footer(2) = ~7 lines)
	bodyHeight := h - 7
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	b.WriteString(m.body(w, bodyHeight))

	// ── Footer ──
	b.WriteString("\n")
	help := "Tab/1-7 views · ↑↓ move · enter expand · e eco · a activity · r refresh · q quit"
	b.WriteString(truncate(styDim.Render(help), w))
	return b.String()
}

func orAllFilter(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

// body renders the active tab into at most maxLines rows.
func (m Model) body(w, maxLines int) string {
	switch m.tab {
	case 0:
		return m.bodyLive(w, maxLines)
	case 1:
		return m.bodyCVEs(w, maxLines)
	case 2:
		return m.bodyNewVuln(w, maxLines)
	case 3:
		return m.bodyPackages(w, maxLines)
	case 4:
		return m.bodyAccess(w, maxLines)
	case 5:
		return m.bodyUpstream(w, maxLines)
	case 6:
		return m.bodyEgress(w, maxLines)
	}
	return ""
}

// window applies the scroll offset and a line budget to a slice index range.
func (m Model) window(total, maxLines int) (start, end int) {
	start = m.scroll
	if start > total {
		start = total
	}
	if start < 0 {
		start = 0
	}
	end = start + maxLines
	if end > total {
		end = total
	}
	return start, end
}

func (m Model) bodyLive(w, maxLines int) string {
	var b strings.Builder
	statsLine := fmt.Sprintf("%s  %s  %s",
		styAllow.Render(fmt.Sprintf("✓ allowed %d", m.live.Allowed)),
		styWarn.Render(fmt.Sprintf("⚠ warned %d", m.live.Warned)),
		styBlock.Render(fmt.Sprintf("✕ blocked %d", m.live.Blocked)),
	)
	b.WriteString(truncate(statsLine, w))
	b.WriteString("\n\n")

	// Filter events first, then window.
	filtered := make([]Event, 0, len(m.events))
	for _, e := range m.events {
		if eventMatches(e, m.eco, m.activity) {
			filtered = append(filtered, e)
		}
	}
	rows := maxLines - 2 // statsLine + blank
	if rows < 1 {
		rows = 1
	}
	if len(filtered) == 0 {
		b.WriteString(styDim.Render("(no events yet)"))
		return b.String()
	}
	start, end := m.window(len(filtered), rows)
	for _, e := range filtered[start:end] {
		ts := "--:--:--"
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Local().Format("15:04:05")
		}
		kind := e.Kind
		if kind == "" {
			kind = "scanned"
		}
		row := fmt.Sprintf("%s  %s  %s  %s  %s",
			styDim.Render(ts),
			styEco.Render(fmt.Sprintf("%-8s", e.Ecosystem)),
			styPkg.Render(fmt.Sprintf("%-40s", trim(e.Package, 40))),
			statusBadge(e.Action),
			styDim.Render(kind),
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) bodyCVEs(w, maxLines int) string {
	if len(m.cves) == 0 {
		return styDim.Render("(no CVEs recorded)")
	}
	var b strings.Builder
	b.WriteString(styHead.Render(fmt.Sprintf("%-9s  %-22s  %-8s  %-30s  %s", "SEVERITY", "ADVISORY", "ECO", "PACKAGE", "VERSION")))
	b.WriteString("\n")
	start, end := m.window(len(m.cves), maxLines-1)
	for _, c := range m.cves[start:end] {
		row := fmt.Sprintf("%-9s  %-22s  %-8s  %-30s  %s",
			sevBadge(c.Severity),
			trim(c.ID, 22),
			styEco.Render(trim(c.Ecosystem, 8)),
			styPkg.Render(trim(c.Package, 30)),
			styDim.Render(orDash(c.Version)),
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) bodyNewVuln(w, maxLines int) string {
	if len(m.newvuln) == 0 {
		return styDim.Render("(no newly-vulnerable packages)")
	}
	var b strings.Builder
	b.WriteString(styHead.Render(fmt.Sprintf("%-24s  %-8s  %-28s  %-12s  %s", "ADVISORIES", "ECO", "PACKAGE", "VERSION", "DOWNLOADS")))
	b.WriteString("\n")
	start, end := m.window(len(m.newvuln), maxLines-1)
	for _, n := range m.newvuln[start:end] {
		advisories := strings.Join(n.Vulns, ",")
		row := fmt.Sprintf("%-24s  %-8s  %-28s  %-12s  %d",
			sevHigh.Render(trim(advisories, 24)),
			styEco.Render(trim(n.Ecosystem, 8)),
			styPkg.Render(trim(n.Package, 28)),
			styDim.Render(trim(orDash(n.Version), 12)),
			n.DownloadCount,
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// pkgRef is one package visible after the eco + activity filters.
type pkgRef struct {
	eco      string
	display  string // namespace/name
	key      string // eco\x00name (matches m.pkgOpen keys)
	versions []TreeVer
}

// visiblePackages flattens the tree into the filtered, ordered package list the
// Packages tab shows: eco filter + at least one version matching the activity
// filter. Order matches the rendered order so m.pkgCursor lines up.
func (m Model) visiblePackages() []pkgRef {
	var out []pkgRef
	for _, eco := range m.tree {
		if m.eco != "" && eco.Ecosystem != m.eco {
			continue
		}
		for _, pkg := range eco.Packages {
			name := pkg.Name
			if pkg.Namespace != "" {
				name = pkg.Namespace + "/" + pkg.Name
			}
			var vs []TreeVer
			for _, v := range pkg.Versions {
				if versionMatches(v.Action, v.Downloaded, m.activity) {
					vs = append(vs, v)
				}
			}
			if len(vs) == 0 {
				continue
			}
			out = append(out, pkgRef{eco: eco.Ecosystem, display: name, key: eco.Ecosystem + "\x00" + name, versions: vs})
		}
	}
	return out
}

func (m Model) bodyPackages(w, maxLines int) string {
	pkgs := m.visiblePackages()
	if len(pkgs) == 0 {
		return styDim.Render("(no packages match the current filters)")
	}
	// Packages are collapsed by default (▸); the selected one (▶ highlight)
	// expands (▾) to show its versions. ↑↓ move the selection, enter toggles.
	var lines []string
	cursorLine := 0
	lastEco := ""
	for i, p := range pkgs {
		if p.eco != lastEco {
			lines = append(lines, styHead.Render(strings.ToUpper(p.eco)))
			lastEco = p.eco
		}
		caret := "▸"
		if m.pkgOpen[p.key] {
			caret = "▾"
		}
		nameCol := trim(p.display, max(w-20, 4))
		count := fmt.Sprintf("(%d ver)", len(p.versions))
		if i == m.pkgCursor {
			cursorLine = len(lines)
			lines = append(lines, styCursor.Render(fmt.Sprintf("  %s %s %s", caret, nameCol, count)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s %s %s", caret, styPkg.Render(nameCol), styDim.Render(count)))
		}
		if m.pkgOpen[p.key] {
			for _, v := range p.versions {
				marker := " "
				if v.Downloaded {
					marker = styOnline.Render("⤓")
				}
				cve := ""
				if v.CVECount > 0 {
					cve = styBlock.Render(fmt.Sprintf(" %d CVE", v.CVECount))
				}
				lines = append(lines, fmt.Sprintf("      %s %s  %s%s",
					marker, styDim.Render(trim(v.Version, 24)), statusBadge(v.Action), cve))
			}
		}
	}
	// Window so the selected package's line stays visible as the cursor moves.
	start := 0
	if cursorLine >= maxLines {
		start = cursorLine - maxLines + 1
	}
	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		start = end
	}
	var b strings.Builder
	for _, ln := range lines[start:end] {
		b.WriteString(truncate(ln, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// versionMatches applies the activity filter to a tree version (the tree has no
// Kind, so "downloaded" keys off the Downloaded flag and "scanned" off action).
func versionMatches(action string, downloaded bool, activity string) bool {
	switch activity {
	case "", "all":
		return true
	case "downloaded":
		return downloaded
	case "blocked":
		return action == "block"
	case "scanned":
		return action != "block"
	default:
		return true
	}
}

func (m Model) bodyAccess(w, maxLines int) string {
	if len(m.access) == 0 {
		return styDim.Render("(no access-log entries)")
	}
	var b strings.Builder
	b.WriteString(styHead.Render(fmt.Sprintf("%-8s  %-20s  %-6s  %-40s  %s", "TIME", "HOST", "METHOD", "PATH", "STATUS")))
	b.WriteString("\n")
	start, end := m.window(len(m.access), maxLines-1)
	for _, a := range m.access[start:end] {
		row := fmt.Sprintf("%-8s  %-20s  %-6s  %-40s  %s",
			styDim.Render(a.Timestamp.Local().Format("15:04:05")),
			trim(a.Host, 20),
			a.Method,
			trim(a.Path, 40),
			statusColor(a.Status),
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) bodyUpstream(w, maxLines int) string {
	if len(m.upstream) == 0 {
		return styDim.Render("(no upstream-log entries)")
	}
	var b strings.Builder
	b.WriteString(styHead.Render(fmt.Sprintf("%-8s  %-8s  %-6s  %-44s  %-6s  %s", "TIME", "ECO", "METHOD", "URL", "STATUS", "MS")))
	b.WriteString("\n")
	start, end := m.window(len(m.upstream), maxLines-1)
	for _, u := range m.upstream[start:end] {
		row := fmt.Sprintf("%-8s  %-8s  %-6s  %-44s  %-6s  %.0f",
			styDim.Render(u.Timestamp.Local().Format("15:04:05")),
			styEco.Render(trim(u.Ecosystem, 8)),
			u.Method,
			trim(u.URL, 44),
			statusColor(u.Status),
			u.MS,
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) bodyEgress(w, maxLines int) string {
	if len(m.egress) == 0 {
		return styDim.Render("(no egress-log entries)")
	}
	var b strings.Builder
	b.WriteString(styHead.Render(fmt.Sprintf("%-8s  %-6s  %-7s  %-30s  %-15s  %s", "TIME", "ACT", "VERB", "HOST", "IP", "REASON")))
	b.WriteString("\n")
	start, end := m.window(len(m.egress), maxLines-1)
	for _, e := range m.egress[start:end] {
		ts := "--:--:--"
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Local().Format("15:04:05")
		}
		var act string
		if e.Action == "block" {
			act = styBlock.Render("✕ block")
		} else {
			act = styAllow.Render("✓ allow")
		}
		row := fmt.Sprintf("%-8s  %-6s  %-7s  %-30s  %-15s  %s",
			styDim.Render(ts),
			act,
			e.Verb,
			trim(e.Host, 30),
			styDim.Render(trim(e.IP, 15)),
			styDim.Render(trim(e.Reason, 40)),
		)
		b.WriteString(truncate(row, w))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func statusColor(code int) string {
	s := fmt.Sprintf("%d", code)
	switch {
	case code >= 500:
		return styBlock.Render(s)
	case code >= 400:
		return styWarn.Render(s)
	default:
		return styAllow.Render(s)
	}
}

// trim shortens a plain (unstyled) string to n runes with an ellipsis.
func trim(s string, n int) string {
	if n <= 0 {
		return "" // guard: width-derived n can be negative on a tiny terminal
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}
