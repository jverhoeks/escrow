package cargo_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestCargoHandler_BlockedCrate_403(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "cargo", Name: "mycrate", Version: "1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	ev := eventlog.New(10)
	h := cargo.New(http.DefaultClient, trust.NewEngine(), pol, c, ev)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/cargo/crates/mycrate/1.0.0/download", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "mycrate@1.0.0", events[0].Package)
}
