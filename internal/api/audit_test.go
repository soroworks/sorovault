package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
)

func TestAuditLogRecordsWritesAndEnforcesAuth(t *testing.T) {
	t.Parallel()

	tokens := []string{
		"ci-bot:0123456789abcdef-ci-secret",
		"0987654321fedcba-ops-secret",
	}
	h := newHarnessWithConfigure(t, func(s *api.Server) {
		s.SetAPITokens(tokens)
	})
	h.fake.Deploy(idA, moduleWith(t, "balance"))

	// GET /api/audit requires Bearer authentication when API tokens are configured.
	resp, raw := h.do(t, http.MethodGet, "/api/audit", "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))

	resp, raw = h.doWithHeader(t, http.MethodGet, "/api/audit", "", "Authorization", "Bearer wrong-token")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, string(raw))

	// 1. Register contract using named token "ci-bot".
	resp, raw = h.doWithHeader(t, http.MethodPost, "/api/contracts",
		`{"contract_id":"`+idA+`"}`, "Authorization", "Bearer 0123456789abcdef-ci-secret")
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	// 2. Refresh contract using unnamed second token ("token-2").
	resp, raw = h.doWithHeader(t, http.MethodPost, "/api/contracts/"+idA+"/refresh",
		"", "Authorization", "Bearer 0987654321fedcba-ops-secret")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	// 3. Edit source using named token "ci-bot".
	resp, raw = h.doWithHeader(t, http.MethodPut, "/api/contracts/"+idA+"/source",
		`{"source_url":"https://github.com/soroworks/sorovault"}`, "Authorization", "Bearer 0123456789abcdef-ci-secret")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	// Query GET /api/audit (newest first).
	resp, raw = h.doWithHeader(t, http.MethodGet, "/api/audit",
		"", "Authorization", "Bearer 0123456789abcdef-ci-secret")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	got := h.decode(t, raw)
	assert.Equal(t, float64(3), got["total"])
	entries := got["entries"].([]any)
	require.Len(t, entries, 3)

	e0 := entries[0].(map[string]any)
	assert.Equal(t, "update_source", e0["action"])
	assert.Equal(t, idA, e0["contract_id"])
	assert.Equal(t, "ci-bot", e0["token_name"])
	assert.NotEmpty(t, e0["client_ip"])
	assert.NotEmpty(t, e0["created_at"])

	e1 := entries[1].(map[string]any)
	assert.Equal(t, "refresh", e1["action"])
	assert.Equal(t, "token-2", e1["token_name"])

	e2 := entries[2].(map[string]any)
	assert.Equal(t, "register", e2["action"])
	assert.Equal(t, "ci-bot", e2["token_name"])

	// Pagination works.
	resp, raw = h.doWithHeader(t, http.MethodGet, "/api/audit?limit=1&offset=1",
		"", "Authorization", "Bearer 0123456789abcdef-ci-secret")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	paged := h.decode(t, raw)
	assert.Equal(t, float64(3), paged["total"])
	pagedEntries := paged["entries"].([]any)
	require.Len(t, pagedEntries, 1)
	assert.Equal(t, "refresh", pagedEntries[0].(map[string]any)["action"])
}
