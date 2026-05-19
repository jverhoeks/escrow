package cireport

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/eventlog"
)

const maxN = 500

// Handler serves GET /ci-report — a GitHub-flavored Markdown summary of all
// packages evaluated in the current session. No authentication required.
type Handler struct {
	log *eventlog.Log
}

func New(log *eventlog.Log) *Handler {
	return &Handler{log: log}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/ci-report", h.handle)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	n := 200
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= maxN {
			n = v
		}
	}

	var since time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		since, _ = time.Parse(time.RFC3339, s)
	}

	events := h.log.Events("")

	if !since.IsZero() {
		var filtered []eventlog.PackageEvent
		for _, e := range events {
			if e.Timestamp.After(since) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	type entry struct {
		eco, pkg, action, reason string
		count                    int
	}
	type key struct{ eco, pkg string }

	seen := map[key]*entry{}
	var order []key

	for _, e := range events {
		if strings.HasPrefix(e.Action, "allowlist-") || strings.HasPrefix(e.Action, "blocklist-") {
			continue
		}
		k := key{e.Ecosystem, e.Package}
		if ex, ok := seen[k]; ok {
			ex.count++
		} else {
			seen[k] = &entry{eco: e.Ecosystem, pkg: e.Package, action: e.Action, reason: e.Reason, count: 1}
			order = append(order, k)
		}
	}

	allowed, blocked, warned := 0, 0, 0
	var blockedList []*entry
	for _, k := range order {
		e := seen[k]
		switch e.action {
		case "allow":
			allowed++
		case "block":
			blocked++
			blockedList = append(blockedList, e)
		case "warn":
			warned++
		}
	}

	var b strings.Builder
	b.WriteString("## Escrow Supply Chain Report\n\n")
	b.WriteString("| | Count |\n|---|---|\n")
	fmt.Fprintf(&b, "| ✅ Allowed | %d |\n", allowed)
	fmt.Fprintf(&b, "| 🚫 Blocked | %d |\n", blocked)
	if warned > 0 {
		fmt.Fprintf(&b, "| ⚠️ Warned | %d |\n", warned)
	}
	b.WriteString("\n")

	if len(blockedList) > 0 {
		b.WriteString("### 🚫 Blocked packages\n\n")
		b.WriteString("| Ecosystem | Package | Reason |\n|---|---|---|\n")
		for _, e := range blockedList {
			reason := e.reason
			if reason == "" {
				reason = "—"
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s |\n", e.eco, e.pkg, reason)
		}
		b.WriteString("\n")
	}

	total := len(order)
	if total == 0 {
		b.WriteString("_No packages evaluated yet._\n")
	} else {
		limit := n
		if total < limit {
			limit = total
		}
		if total > n {
			fmt.Fprintf(&b, "### All packages (showing %d of %d)\n\n", n, total)
		} else {
			fmt.Fprintf(&b, "### All packages (%d)\n\n", total)
		}
		b.WriteString("| # | Ecosystem | Package | Status | Reason |\n|---|---|---|---|---|\n")
		for i, k := range order[:limit] {
			e := seen[k]
			status := e.action
			switch e.action {
			case "allow":
				status = "✅"
			case "block":
				status = "🚫"
			case "warn":
				status = "⚠️"
			}
			reason := e.reason
			if reason == "" {
				reason = "—"
			}
			fmt.Fprintf(&b, "| %d | %s | `%s` | %s | %s |\n", i+1, e.eco, e.pkg, status, reason)
		}
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(b.String())) //nolint:errcheck
}
