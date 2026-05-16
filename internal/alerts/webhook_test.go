package alerts_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestWebhook_PostsOnBlock(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	wh := alerts.NewWebhook(srv.URL, srv.Client())
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "evil-pkg", Version: "1.0.0", PublishedAt: time.Now()}
	d := policy.Decision{Action: policy.ActionBlock, Signal: "age", Reason: "1 day old"}

	err := wh.Send(pkg, d)
	require.NoError(t, err)
	assert.Equal(t, "evil-pkg", received["package"])
	assert.Equal(t, "block", received["action"])
	assert.Equal(t, "age", received["signal"])
}

func TestWebhook_SkipsOnWarnOrAllow(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()

	wh := alerts.NewWebhook(srv.URL, srv.Client())
	pkg := trust.Package{Name: "pkg"}
	wh.Send(pkg, policy.Decision{Action: policy.ActionWarn})
	wh.Send(pkg, policy.Decision{Action: policy.ActionAllow})
	assert.Equal(t, 0, calls, "webhook should only fire on block")
}
