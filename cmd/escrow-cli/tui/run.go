package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/jverhoeks/escrow/internal/config"
)

// Options configures the TUI.
type Options struct {
	URL      string // explicit base URL ("" → discover via runtime.json)
	User     string
	Password string
	Path     string // event-log path for offline tail ("" → discover)
}

// Run builds the client, attempts login, and starts the program. On failure it
// falls back to offline mode (event-log tail for the Live view).
func Run(opts Options) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("escrow-cli tui needs an interactive terminal; use `escrow-cli live` for piped output")
	}
	client, mode := buildClient(opts)
	m := NewModel(client)
	m.status = mode
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Live stream (online) or file tail (offline) feeds events into the program.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startFeed(ctx, client, opts, p)

	_, err := p.Run()
	return err
}

// buildClient resolves the base URL + credentials, logs in, and returns the
// connected client. On any failure it returns (nil, "offline: <reason>") so the
// caller starts in offline mode.
func buildClient(opts Options) (*Client, string) {
	base := opts.URL
	if base == "" {
		port := 7888
		if rt, err := config.ReadRuntime(); err == nil && rt.Port > 0 {
			port = rt.Port
		}
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	user, pass, dashPath := opts.User, opts.Password, ""
	if user == "" || pass == "" || dashPath == "" {
		if cfg, ok := loadDiscoveredConfig(); ok {
			if user == "" {
				user = cfg.Dashboard.Username
			}
			if pass == "" {
				pass = cfg.Dashboard.Password
			}
			dashPath = cfg.Dashboard.Path
		}
	}

	c, err := NewClient(base, dashPath, user, pass)
	if err != nil {
		return nil, "offline: " + err.Error()
	}
	if err := c.Login(); err != nil {
		return nil, "offline: " + err.Error()
	}
	return c, "connected to " + base
}

// loadDiscoveredConfig searches the common escrow.toml locations and returns the
// first one that exists.
func loadDiscoveredConfig() (config.Config, bool) {
	var candidates []string
	if p := os.Getenv("ESCROW_CONFIG"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		"/opt/homebrew/etc/escrow/escrow.toml",
		"/usr/local/etc/escrow/escrow.toml",
		"escrow.toml",
	)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "escrow", "escrow.toml"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			if cfg, err := config.Load(p); err == nil {
				return cfg, true
			}
		}
	}
	return config.Config{}, false
}

// startFeed wires the event source into the program: an SSE stream when online,
// or a JSONL file tail when offline.
func startFeed(ctx context.Context, client *Client, opts Options, p *tea.Program) {
	if client != nil {
		go streamOnline(ctx, client, p)
		return
	}
	go tailOffline(ctx, opts, p)
}

// streamOnline reads the SSE stream and forwards each event to the program,
// reconnecting with a fixed backoff if the stream drops (proxy restart, network
// blip) until ctx is cancelled.
func streamOnline(ctx context.Context, client *Client, p *tea.Program) {
	const backoff = 3 * time.Second
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		ch, err := client.Stream(ctx)
		if err != nil {
			p.Send(connMsg{"reconnecting…"})
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		if !first {
			p.Send(connMsg{"reconnected"})
		} else {
			first = false
		}
		// Drain until the stream closes or ctx is cancelled.
		dropped := false
		for !dropped {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					dropped = true
					break
				}
				p.Send(streamMsg{e})
			}
		}
		p.Send(connMsg{"reconnecting…"})
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// sleepCtx waits for d or until ctx is cancelled. Returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// tailOffline tails the event-log JSONL from the end, forwarding new events. It
// is rotation-safe: if the file shrinks (truncated or replaced by log rotation)
// it re-opens from the start so it doesn't get stuck past the new EOF.
func tailOffline(ctx context.Context, opts Options, p *tea.Program) {
	path := opts.Path
	if path == "" {
		if rt, err := config.ReadRuntime(); err == nil && rt.EventLogPath != "" {
			path = rt.EventLogPath
		} else {
			path = filepath.Join("escrow-cache", "escrow-events.jsonl")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { f.Close() }()
	pos, _ := f.Seek(0, io.SeekEnd) // start tailing from the end
	reader := bufio.NewReader(f)
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			pos += int64(len(line)) // partial line read; remember bytes consumed
			// Detect rotation: if the file is now smaller than our position, the
			// file was truncated or replaced — re-open from the start.
			if fi, statErr := os.Stat(path); statErr == nil && fi.Size() < pos {
				f.Close()
				nf, openErr := os.Open(path)
				if openErr != nil {
					return
				}
				f = nf
				pos = 0
				reader = bufio.NewReader(f)
				continue
			}
			if !sleepCtx(ctx, 500*time.Millisecond) {
				return
			}
			continue
		}
		if err != nil {
			return
		}
		pos += int64(len(line))
		var e Event
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) != nil {
			continue
		}
		p.Send(streamMsg{e})
	}
}
