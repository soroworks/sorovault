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
