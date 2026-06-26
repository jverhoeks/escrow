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
	// The transport is wrapped for upstream-error metering; unwrap to the base.
	ect, ok := c.Transport.(*errorCountingTransport)
	if !ok {
		t.Fatalf("Transport is %T, want *errorCountingTransport", c.Transport)
	}
	tr, ok := ect.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport is %T, want *http.Transport", ect.base)
	}
	if tr.MaxIdleConns != 256 {
		t.Fatalf("MaxIdleConns = %d, want 256", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 20 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}
}
