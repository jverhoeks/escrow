package upstream_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jverhoeks/escrow/internal/upstream"
)

func BenchmarkNewClient(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := upstream.New()
	b.ResetTimer()
	for range b.N {
		resp, err := c.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkNewClient_Parallel(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := upstream.New()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := c.Get(srv.URL)
			if err != nil {
				b.Fatal(err)
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	})
}

func BenchmarkMetadataClient(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"test","versions":{"1.0.0":{}}}`))
	}))
	defer srv.Close()

	base := upstream.New()
	mc := upstream.MetadataClient(base)
	b.ResetTimer()
	for range b.N {
		resp, err := mc.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkReadBody_Small(b *testing.B) {
	data := []byte(`{"name":"lodash","versions":{"4.17.21":{"name":"lodash","version":"4.17.21"}}}`)
	for range b.N {
		r := bytes.NewReader(data)
		out, err := upstream.ReadBody(r)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != len(data) {
			b.Fatalf("got %d bytes, want %d", len(out), len(data))
		}
	}
}

func BenchmarkReadBody_Large(b *testing.B) {
	// Simulate a large manifest (1 MB)
	data := bytes.Repeat([]byte(`{"a":"b"}`), 256*1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(data)
		out, err := upstream.ReadBodyLimit(r, int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != len(data) {
			b.Fatalf("got %d bytes, want %d", len(out), len(data))
		}
	}
}

func BenchmarkReadBody_OverLimit(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 1024)
	limit := int64(512)
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(data)
		_, err := upstream.ReadBodyLimit(r, limit)
		if err == nil {
			b.Fatal("expected error for over-limit read")
		}
	}
}
