package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterWithName proves POST /api/contracts stores an optional name,
// trimmed, and that registering again without one keeps it.
func TestRegisterWithName(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))

	resp, raw := h.do(t, http.MethodPost, "/api/contracts",
		`{"contract_id":"`+idA+`","name":"  Example Token  "}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	contract := h.decode(t, raw)["contract"].(map[string]any)
	assert.Equal(t, "Example Token", contract["name"], "the name is trimmed and stored")

	// A plain re-register — what a client refreshing a contract sends —
	// must not clear the name.
	resp, raw = h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	contract = h.decode(t, raw)["contract"].(map[string]any)
	assert.Equal(t, "Example Token", contract["name"])

	// Nor must an explicit refresh.
	resp, raw = h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	contract = h.decode(t, raw)["contract"].(map[string]any)
	assert.Equal(t, "Example Token", contract["name"])

	resp, raw = h.do(t, http.MethodGet, "/api/contracts?q=example", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Contains(t, string(raw), idA, "search matches the name")
}

// TestRenameContract proves PATCH /api/contracts/{id} replaces the name.
func TestRenameContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))
	resp, _ := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`","name":"Old"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, raw := h.do(t, http.MethodPatch, "/api/contracts/"+idA, `{"name":" New Name "}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Equal(t, "New Name", h.decode(t, raw)["name"])

	resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Equal(t, "New Name", h.decode(t, raw)["name"])

	resp, raw = h.do(t, http.MethodPatch, "/api/contracts/"+idB, `{"name":"Nobody"}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
}

// TestContractNameValidation proves names are length-limited, may not hold
// control characters, and that a rename must carry a non-empty name.
func TestContractNameValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))
	resp, _ := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`","name":"Kept"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	tooLong := strings.Repeat("n", 101)
	exactly100 := strings.Repeat("é", 100)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"register: too long", http.MethodPost, "/api/contracts", `{"contract_id":"` + idA + `","name":"` + tooLong + `"}`, http.StatusBadRequest},
		{"register: control character", http.MethodPost, "/api/contracts", `{"contract_id":"` + idA + `","name":"bad\u0007name"}`, http.StatusBadRequest},
		{"register: newline", http.MethodPost, "/api/contracts", `{"contract_id":"` + idA + `","name":"two\nlines"}`, http.StatusBadRequest},
		{"rename: too long", http.MethodPatch, "/api/contracts/" + idA, `{"name":"` + tooLong + `"}`, http.StatusBadRequest},
		{"rename: control character", http.MethodPatch, "/api/contracts/" + idA, `{"name":"tab\there"}`, http.StatusBadRequest},
		{"rename: empty", http.MethodPatch, "/api/contracts/" + idA, `{"name":""}`, http.StatusBadRequest},
		{"rename: only spaces", http.MethodPatch, "/api/contracts/" + idA, `{"name":"   "}`, http.StatusBadRequest},
		{"rename: no name field", http.MethodPatch, "/api/contracts/" + idA, `{}`, http.StatusBadRequest},
		{"rename: unknown field", http.MethodPatch, "/api/contracts/" + idA, `{"name":"x","label":"y"}`, http.StatusBadRequest},
		{"rename: bad contract id", http.MethodPatch, "/api/contracts/not-a-contract", `{"name":"x"}`, http.StatusBadRequest},
		{"rename: 100 multibyte characters", http.MethodPatch, "/api/contracts/" + idA, `{"name":"` + exactly100 + `"}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, raw := h.do(t, tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, resp.StatusCode, string(raw))
		})
	}

	// None of the rejected writes reached the store.
	got, err := h.store.GetContract(t.Context(), "testnet", idA)
	require.NoError(t, err)
	assert.Equal(t, exactly100, got.Name)
}
