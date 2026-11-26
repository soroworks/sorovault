package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/soroworks/sorovault/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPMethodSearch(t *testing.T) {
	t.Parallel()

	params := &mcp.Params{
		Method: mcp.MethodSearch,
		ID:     1,
		Data:   json.RawMessage(`{"query": "test"}`),
	}

	server := mcp.NewServer()
	result, err := server.Handle(context.Background(), params.Method, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &resp))
	require.Equal(t, "ok", resp["status"])
}

func TestMCPMethodInterface(t *testing.T) {
	t.Parallel()

	params := &mcp.Params{
		Method: mcp.MethodInterface,
		ID:     2,
		Data:   json.RawMessage(`{"contract_id": "ST123456789012345678901234567890123456"}`),
	}

	server := mcp.NewServer()
	result, err := server.Handle(context.Background(), params.Method, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &resp))
	require.Equal(t, "ok", resp["status"])
}

func TestMCPMethodClient(t *testing.T) {
	t.Parallel()

	params := &mcp.Params{
		Method: mcp.MethodClient,
		ID:     3,
		Data:   json.RawMessage(`{"contract_id": "ST123456789012345678901234567890123456"}`),
	}

	server := mcp.NewServer()
	result, err := server.Handle(context.Background(), params.Method, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &resp))
	require.Equal(t, "ok", resp["status"])
}

func TestMCPHandleEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      mcp.Method
		params      *mcp.Params
		expectError bool
	}{
		{
			name:        "unknown method",
			method:      mcp.Method("unknown"),
			params:      &mcp.Params{Method: mcp.Method("unknown"), ID: 1, Data: json.RawMessage(`{}`)},
			expectError: true,
		},
		{
			name:        "nil params",
			method:      mcp.MethodSearch,
			params:      nil,
			expectError: true,
		},
		{
			name:        "invalid json data",
			method:      mcp.MethodSearch,
			params:      &mcp.Params{Method: mcp.MethodSearch, ID: 2, Data: json.RawMessage(`invalid-json`)},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := mcp.NewServer()
			_, err := server.Handle(context.Background(), tc.method, tc.params)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMCPErrorJSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := mcp.MCPError{Code: -32601, Message: "method not found"}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded mcp.MCPError
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded, "MCPError must survive a JSON round trip unchanged")
}

func TestResultJSONRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		original mcp.Result
	}{
		{
			name: "success response",
			original: mcp.Result{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"status":"ok"}`),
			},
		},
		{
			// A nil json.RawMessage marshals as null and decodes back as the
			// literal null rather than nil, so spell it out to keep the
			// comparison exact.
			name: "error response",
			original: mcp.Result{
				JSONRPC: "2.0",
				ID:      2,
				Result:  json.RawMessage(`null`),
				Error:   &mcp.MCPError{Code: -32601, Message: "method not found"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.original)
			require.NoError(t, err)

			var decoded mcp.Result
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, tc.original, decoded, "Result must survive a JSON round trip unchanged")
		})
	}
}
