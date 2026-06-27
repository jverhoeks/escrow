package nuget_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

// #100: a concurrent registration + version-list request for the same uncached
// package must collapse into ONE upstream registration fetch (shared singleflight).
func TestNuGetHandler_RegistrationSingleflightDedup(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "registration5-semver1") {
			atomic.AddInt32(&hits, 1)
			time.Sleep(80 * time.Millisecond) // widen the in-flight window
			json.NewEncoder(w).Encode(makeRegistration([]struct {
				Ver       string
				Published time.Time
			}{{Ver: "1.0.0", Published: time.Now().AddDate(-1, 0, 0)}}))
			return
		}
	}))
	defer upstream.Close()

	h := makeHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	paths := []string{
		"/nuget/v3/registration5-semver1/newtonsoft.json/index.json",
		"/nuget/v3-flatcontainer/newtonsoft.json/index.json",
	}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		}(p)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "concurrent registration + version-list should share one upstream fetch")
}
