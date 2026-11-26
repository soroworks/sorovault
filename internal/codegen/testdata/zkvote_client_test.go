package zkvote_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/sorovault/internal/codegen/testdata"
)

func TestZkVoteClientGetProposalCount(t *testing.T) {
	fn := func() {
		_ = zkvote.NewClient("invalid-contract-id", "https://soroban-testnet.stellar.org:443")
	}
	assert.NotPanics(t, fn)
}

func TestZkVoteClientGetProposalCountHappyAndErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		contractID string
		wantError  bool
	}{
		{
			name:       "happy path with valid contract id",
			contractID: "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
			wantError:  false,
		},
		{
			name:       "error path with invalid contract id",
			contractID: "invalid-contract-id",
			wantError:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := zkvote.NewClient(tt.contractID, "https://soroban-testnet.stellar.org:443")

			count, err := client.GetProposalCount()
			if tt.wantError {
				assert.Error(t, err)
			} else {
				_ = count
			}
		})
	}
}
