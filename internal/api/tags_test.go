package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/store"
)

func TestContractTagsAndFiltering(t *testing.T) {
	t.Parallel()

	h := newSourceUpdateHarness(t)

	// Set tags via PUT /api/contracts/{id}/tags.
	resp, raw := h.do(t, http.MethodPut, "/api/contracts/"+idA+"/tags", `{"tags":["DEX","Oracle","dex"]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	got := h.decode(t, raw)
	assert.Equal(t, []any{"dex", "oracle"}, got["tags"])

	// GET /api/contracts/{id} includes tags.
	resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	got = h.decode(t, raw)
	assert.Equal(t, []any{"dex", "oracle"}, got["tags"])

	// Filter via GET /api/contracts?tag=...
	resp, raw = h.do(t, http.MethodGet, "/api/contracts?tag=dex", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	list := h.decode(t, raw)
	assert.Equal(t, float64(1), list["total"])

	resp, raw = h.do(t, http.MethodGet, "/api/contracts?tag=governance", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	list = h.decode(t, raw)
	assert.Equal(t, float64(0), list["total"])

	// Invalid tags are rejected with 400.
	resp, raw = h.do(t, http.MethodPut, "/api/contracts/"+idA+"/tags", `{"tags":["invalid tag!"]}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))

	// Unknown contract returns 404.
	resp, raw = h.do(t, http.MethodPut, "/api/contracts/"+idB+"/tags", `{"tags":["dex"]}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
}

func TestTokenContractAutoTagged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	functions := make([]model.Function, 0, len(spec.SEP41.Functions))
	for _, required := range spec.SEP41.Functions {
		inputs := make([]model.Param, 0, len(required.Inputs))
		for i, typ := range required.Inputs {
			inputs = append(inputs, model.Param{Name: string(rune('a' + i)), Type: typ})
		}
		functions = append(functions, model.Function{Name: required.Name, Inputs: inputs, Outputs: append([]model.Type(nil), required.Outputs...)})
	}
	require.NoError(t, h.store.Save(context.Background(), store.Contract{
		ContractID:      idA,
		Network:         "testnet",
		CurrentWasmHash: "token-hash",
	}, &model.Interface{ABIVersion: model.ABIVersion, Functions: functions}))

	resp, raw := h.do(t, http.MethodGet, "/api/contracts?tag=token", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	list := h.decode(t, raw)
	assert.Equal(t, float64(1), list["total"])
}
