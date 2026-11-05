package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractNamingAndPatch(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))

	// Register with a name via POST /api/contracts.
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`","name":"  Example Token  "}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	got := h.decode(t, raw)["contract"].(map[string]any)
	assert.Equal(t, "Example Token", got["name"])

	// Refresh does not clear the name.
	resp, raw = h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	got = h.decode(t, raw)["contract"].(map[string]any)
	assert.Equal(t, "Example Token", got["name"])

	// Rename via PATCH /api/contracts/{id}.
	resp, raw = h.do(t, http.MethodPatch, "/api/contracts/"+idA, `{"name":"Renamed Token"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Equal(t, "Renamed Token", h.decode(t, raw)["name"])

	// Validation errors return 400.
	for _, body := range []string{
		`{}`,
		`{"name":"   "}`,
		`{"name":"` + strings.Repeat("x", 101) + `"}`,
		`{"name":"control\u0007char"}`,
	} {
		resp, raw = h.do(t, http.MethodPatch, "/api/contracts/"+idA, body)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
	}

	// Unknown contract returns 404.
	resp, raw = h.do(t, http.MethodPatch, "/api/contracts/"+idB, `{"name":"Nope"}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
}
