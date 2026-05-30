package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFaviconHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	faviconHandler(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("content-type = %q, want image/svg+xml", ct)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatalf("body is not SVG: %q", rec.Body.String())
	}
}
