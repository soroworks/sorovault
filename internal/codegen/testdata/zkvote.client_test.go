package zkvote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetProposalHappyPath confirms that a client built with the canonical
// contract address (the one the file was generated for) can encode
// GetProposal without error. The returned operation is non-nil and carries
// the invoke contract function call.
func TestGetProposalHappyPath(t *testing.T) {
	t.Parallel()

	client := NewClient(ContractID, Network)
	op, err := client.GetProposal(0)
	require.NoError(t, err, "GetProposal with a valid contract ID must not fail")
	assert.NotNil(t, op, "GetProposal must return a non-nil operation")
}

// TestGetProposalInvalidContractID confirms that an invalid contract address
// is caught when the operation is built, not silently ignored.
func TestGetProposalInvalidContractID(t *testing.T) {
	t.Parallel()

	client := &Client{ContractID: "not-a-valid-strkey", Network: Network}
	op, err := client.GetProposal(0)
	require.Error(t, err, "GetProposal with an invalid contract ID must return an error")
	assert.Nil(t, op, "GetProposal must return nil operation on error")
}

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

func TestHasVoted(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		wantErr    bool
	}{
		{
			name:       "valid contract ID returns an operation",
			contractID: ContractID,
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
			op, err := client.HasVoted([32]byte{})
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
