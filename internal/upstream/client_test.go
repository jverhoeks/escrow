package upstream

import (
	"net/http"
	"testing"
)

// TestNew_MaxIdleConns asserts the global idle-connection cap is large enough
// that one busy ecosystem can't exhaust the shared pool and starve the others
// (7 ecosystems × MaxIdleConnsPerHost 20 = 140 potential).
func TestNew_MaxIdleConns(t *testing.T) {
	c := New()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.MaxIdleConns != 256 {
		t.Fatalf("MaxIdleConns = %d, want 256", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 20 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}
}
