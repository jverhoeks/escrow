package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jverhoeks/escrow/internal/config"
)

type liveEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
}

// liveMatch reports whether e passes the --eco and --activity filters.
func liveMatch(e liveEvent, eco, activity string) bool {
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

func runLive(args []string) {
	fs := flag.NewFlagSet("live", flag.ExitOnError)
	eco := fs.String("eco", "", "filter by ecosystem (npm, pypi, cargo, go, ...)")
	activity := fs.String("activity", "all", "all|downloaded|scanned|blocked")
	path := fs.String("path", "", "event-log JSONL path (default: ./escrow-cache/escrow-events.jsonl)")
	fs.Parse(args) //nolint:errcheck

	p := *path
	if p == "" {
		// Prefer the running proxy's published event-log path (any CWD).
		if rt, err := config.ReadRuntime(); err == nil && rt.EventLogPath != "" {
			p = rt.EventLogPath
		} else {
			p = filepath.Join("escrow-cache", "escrow-events.jsonl")
		}
	}
	f, err := os.Open(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli live: cannot open event log %s: %v\n", p, err)
		fmt.Fprintln(os.Stderr, "is escrow running with event-log persistence (disk backend, or eventlog_path)? or pass --path")
		os.Exit(1)
	}
	defer f.Close()

	// Tail from the end so we show new activity.
	f.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(f)
	fmt.Printf("watching %s  (eco=%s activity=%s) — Ctrl-C to stop\n", p, orAll(*eco), *activity)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return
		}
		var e liveEvent
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) != nil {
			continue
		}
		if !liveMatch(e, *eco, *activity) {
			continue
		}
		fmt.Println(formatLive(e))
	}
}

func orAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

func formatLive(e liveEvent) string {
	st := "✓ allow"
	switch {
	case e.Action == "block":
		st = "✕ block"
	case e.Action == "warn":
		st = "⚠ warn"
	}
	kind := e.Kind
	if kind == "" {
		kind = "scanned"
	}
	return fmt.Sprintf("%s  %-8s  %-9s  %-40s  %s",
		e.Timestamp.Local().Format("15:04:05"), e.Ecosystem, st, e.Package, kind)
}
