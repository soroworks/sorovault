package zkvote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetVoteParams covers the happy path (valid contract ID returns an
// operation) and the error path (an invalid ID fails at the strkey layer).
func TestGetVoteParams(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		wantErr    bool
	}{
		{
			name:       "valid contract ID returns an operation",
			contractID: ContractID,
			wantErr:    false,
		},
		{
			name:       "invalid contract ID returns an error",
			contractID: "not-a-valid-contract-id",
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
				client := NewClient(tt.contractID, Network)
			op, err := client.GetVoteParams()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, op)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, op)
			}
		})
	}
}
