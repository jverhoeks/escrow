package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// readBodyHandler reads the entire request body and translates a
// *http.MaxBytesError (from MaxBytesReader) into a 413. MaxBytesReader does not
// write a status itself — the read past the limit returns the error and the
// handler must respond.
func readBodyHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := io.ReadAll(r.Body); err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestMaxRequestBodyMiddleware(t *testing.T) {
	r := chi.NewRouter()
	r.Use(maxRequestBodyMiddleware(1)) // 1 MB limit
	r.Post("/", readBodyHandler)

	srv := httptest.NewServer(r)
	defer srv.Close()

	t.Run("over limit -> 413", func(t *testing.T) {
		body := bytes.Repeat([]byte("x"), 2*1024*1024) // 2 MB
		resp, err := http.Post(srv.URL+"/", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", resp.StatusCode)
		}
	})

	t.Run("under limit -> 200", func(t *testing.T) {
		body := bytes.Repeat([]byte("x"), 1024) // 1 KB
		resp, err := http.Post(srv.URL+"/", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}
