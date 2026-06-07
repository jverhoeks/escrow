package trust_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestPopularitySignal_Spike(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()

	// Pre-load a near-zero baseline (encoded as {"downloads":10})
	baselineJSON, _ := json.Marshal(map[string]int{"downloads": 10})
	c.SetMeta(ctx, "pop/npm/spiking-pkg/baseline", baselineJSON, time.Hour)

	npmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"downloads": 1200})
	}))
	defer npmSrv.Close()

	_ = trust.NewPopularitySignal(10.0, http.DefaultClient, c, npmSrv.URL, "")
	// Override the client to use the test server
	sig2 := trust.NewPopularitySignal(10.0, npmSrv.Client(), c, npmSrv.URL, "")
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "spiking-pkg", Version: "1.0.0"}
	report, err := sig2.Check(ctx, pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalWarn, report.Result)
	assert.Contains(t, report.Reason, "spike")
}

func TestPopularitySignal_NoBaseline_Skips(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	npmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"downloads": 500})
	}))
	defer npmSrv.Close()
	sig := trust.NewPopularitySignal(10.0, npmSrv.Client(), c, npmSrv.URL, "")
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "new-pkg", Version: "1.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalSkip, report.Result)
}

// TestPopularitySignal_MalformedBody_Errors verifies that a 200 response with a
// malformed JSON body surfaces as SignalError, not Skip/Pass. A swallowed decode
// error would yield (0, nil) downloads and silently bypass the SignalError path.
func TestPopularitySignal_MalformedBody_Errors(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	npmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("this is not json {{{"))
	}))
	defer npmSrv.Close()
	sig := trust.NewPopularitySignal(10.0, npmSrv.Client(), c, npmSrv.URL, "")
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "garbage-pkg", Version: "1.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalError, report.Result,
		"a malformed 200 body should surface as SignalError, not be swallowed")
}
