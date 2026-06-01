// Package tui implements `escrow-cli tui`, an interactive terminal dashboard
// that reads the running proxy's authenticated API (with an offline event-log
// fallback handled in run.go).
package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client talks to a running escrow dashboard API using a session cookie.
type Client struct {
	base string // e.g. http://127.0.0.1:7888
	path string // dashboard path, e.g. /dashboard
	user string
	pass string
	http *http.Client
}

func NewClient(base, dashPath, user, pass string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if dashPath == "" {
		dashPath = "/dashboard"
	}
	return &Client{
		base: strings.TrimRight(base, "/"),
		path: dashPath,
		user: user, pass: pass,
		http: &http.Client{Timeout: 10 * time.Second, Jar: jar},
	}, nil
}

// Login posts the credentials and stores the session cookie in the jar.
func (c *Client) Login() error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	// Don't follow the post-login redirect (we only need the Set-Cookie).
	noRedirect := *c.http
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Post(c.base+c.path+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()
	u, _ := url.Parse(c.base)
	for _, ck := range resp.Cookies() {
		if ck.Name == "escrow_session" && ck.Value != "" {
			c.http.Jar.SetCookies(u, []*http.Cookie{ck})
			return nil
		}
	}
	return fmt.Errorf("login failed: no session cookie (check credentials)")
}

func (c *Client) getJSON(path string, out any) error {
	resp, err := c.http.Get(c.base + c.path + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		return fmt.Errorf("unauthorized (HTTP %d) — not logged in", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── Typed responses (mirror the dashboard JSON) ───────────────────────────────

type Stats struct {
	Blocked    int `json:"blocked"`
	Warned     int `json:"warned"`
	Allowed    int `json:"allowed"`
	TopBlocked []struct {
		Package string `json:"package"`
		Count   int    `json:"count"`
	} `json:"top_blocked"`
}

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Signal    string    `json:"signal"`
	Reason    string    `json:"reason"`
	Kind      string    `json:"kind"`
}

type CVE struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	Version   string `json:"version"`
}

type NewVuln struct {
	Ecosystem     string   `json:"ecosystem"`
	Package       string   `json:"package"`
	Version       string   `json:"version"`
	Vulns         []string `json:"vulns"`
	DownloadCount int      `json:"download_count"`
}

type TreeEco struct {
	Ecosystem string `json:"ecosystem"`
	Packages  []struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Versions  []struct {
			Version       string `json:"version"`
			Action        string `json:"action"`
			Downloaded    bool   `json:"downloaded"`
			DownloadCount int    `json:"download_count"`
			CVECount      int    `json:"cve_count"`
		} `json:"versions"`
	} `json:"packages"`
}

type AccessEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
}

type UpstreamEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	MS        float64   `json:"ms"`
}

func (c *Client) Stats() (Stats, error)               { var s Stats; return s, c.getJSON("/api/stats", &s) }
func (c *Client) Events(n int) ([]Event, error)       { var e []Event; return e, c.getJSON(fmt.Sprintf("/api/events?n=%d", n), &e) }
func (c *Client) CVEs() ([]CVE, error)                { var v []CVE; return v, c.getJSON("/api/cves", &v) }
func (c *Client) NewlyVulnerable() ([]NewVuln, error) { var v []NewVuln; return v, c.getJSON("/api/newly-vulnerable", &v) }
func (c *Client) PackagesTree() ([]TreeEco, error)    { var t []TreeEco; return t, c.getJSON("/api/packages/tree", &t) }
func (c *Client) AccessLog(n int) ([]AccessEntry, error) {
	var a []AccessEntry
	return a, c.getJSON(fmt.Sprintf("/api/accesslog?n=%d", n), &a)
}
func (c *Client) UpstreamLog(n int) ([]UpstreamEntry, error) {
	var u []UpstreamEntry
	return u, c.getJSON(fmt.Sprintf("/api/upstreamlog?n=%d", n), &u)
}

// Stream connects to the SSE endpoint and emits each event on the returned
// channel until ctx is cancelled. Lines are "data: {json}" (comments ignored).
func (c *Client) Stream(ctx context.Context) (<-chan Event, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+c.path+"/api/stream", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream HTTP %d", resp.StatusCode)
	}
	ch := make(chan Event, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil {
				select {
				case ch <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}
