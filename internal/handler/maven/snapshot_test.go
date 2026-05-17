package maven_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/handler/maven"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestMavenHandler_SnapshotRoutedToSnapshotUpstream(t *testing.T) {
	var releaseHit, snapshotHit int64

	releaseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&releaseHit, 1)
		w.Write([]byte("release bytes"))
	}))
	defer releaseSrv.Close()

	snapshotSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&snapshotHit, 1)
		w.Write([]byte("snapshot bytes"))
	}))
	defer snapshotSrv.Close()

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"docs": []any{}}})
	}))
	defer searchSrv.Close()

	c := cache.NewMemory()
	h := maven.New(releaseSrv.Client(), releaseSrv.URL, trust.NewEngine(), policy.New(nil), c, nil)
	h.SetSnapshotURL(snapshotSrv.URL)
	h.SetSearchURL(searchSrv.URL)

	r := chi.NewRouter()
	h.Mount(r)

	// SNAPSHOT request should go to snapshot server
	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0-SNAPSHOT/mylib-1.0-SNAPSHOT.jar", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	assert.Equal(t, int64(0), atomic.LoadInt64(&releaseHit), "release server should not be hit for SNAPSHOT request")
	assert.Equal(t, int64(1), atomic.LoadInt64(&snapshotHit), "snapshot server should be hit for SNAPSHOT request")

	// Release request should go to release server
	req2 := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0/mylib-1.0.jar", nil)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	require.Equal(t, http.StatusOK, rr2.Code)

	assert.Equal(t, int64(1), atomic.LoadInt64(&releaseHit), "release server should be hit for release request")
}

func TestMavenHandler_SnapshotMetadataCacheHit(t *testing.T) {
	var hitCount int64

	snapshotSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitCount, 1)
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?>
<metadata>
  <groupId>com.example</groupId>
  <artifactId>mylib</artifactId>
  <versioning>
    <snapshot><timestamp>20240101.000000</timestamp><buildNumber>1</buildNumber></snapshot>
    <lastUpdated>20240101000000</lastUpdated>
  </versioning>
</metadata>`))
	}))
	defer snapshotSrv.Close()

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"docs": []any{}}})
	}))
	defer searchSrv.Close()

	c := cache.NewMemory()
	h := maven.New(snapshotSrv.Client(), snapshotSrv.URL, trust.NewEngine(), policy.New(nil), c, nil)
	h.SetSnapshotURL(snapshotSrv.URL)
	h.SetSearchURL(searchSrv.URL)

	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0-SNAPSHOT/maven-metadata.xml", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&hitCount), "second SNAPSHOT metadata request should come from cache")
}
