package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCVEs_ListsBlockedByVuln(t *testing.T) {
	handler, _ := newTestDashboardWithEvents(t) // includes one osv-blocked event with GHSA-x
	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/cves", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []struct {
		ID        string `json:"id"`
		Severity  string `json:"severity"`
		Ecosystem string `json:"ecosystem"`
		Package   string `json:"package"`
		Version   string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	require.Equal(t, "GHSA-x", out[0].ID)
	require.Equal(t, "HIGH", out[0].Severity)
	require.Equal(t, "b", out[0].Package)
}
