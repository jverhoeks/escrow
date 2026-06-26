package maven_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/maven"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestMavenHandler_BlockedArtifact_403(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	bl, err := block.New("")
	require.NoError(t, err)
	// mavenCoordsFromPath derives "groupId:artifactId" + version from the layout.
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "maven", Name: "com.example:mylib", Version: "1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	ev := eventlog.New(10)
	h := maven.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, ev)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0.0/mylib-1.0.0.jar", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "com.example:mylib@1.0.0", events[0].Package)
}

// A blocked artifact must not be served even from a warm cache — the download
// gate runs before the blob lookup. This is the core gap #35 closes.
func TestMavenHandler_BlockedArtifact_403_CacheHit(t *testing.T) {
	const upstreamURL = "http://127.0.0.1:0"
	const path = "com/example/mylib/1.0.0/mylib-1.0.0.jar"
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "maven/artifacts/"+upstreamURL+"/"+path, strings.NewReader("CACHED-JAR")))
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "maven", Name: "com.example:mylib", Version: "1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	h := maven.New(http.DefaultClient, upstreamURL, trust.NewEngine(), pol, c, eventlog.New(10))

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/maven2/"+path, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "CACHED-JAR")
}
