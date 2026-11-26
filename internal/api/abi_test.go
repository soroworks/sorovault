package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/sorovault/internal/api"
)

func TestABIFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		contractID string
		wasmHash   string
		want       string
	}{
		{
			name:       "happy path with full hashes",
			contractID: idA,
			wasmHash:   "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
			want:       "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN.c618dae26486.abi.json",
		},
		{
			name:       "error path with invalid contract id",
			contractID: "invalid-id",
			wasmHash:   "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
			want:       "invalid-id.c618dae26486.abi.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := api.ABIFilename(tt.contractID, tt.wasmHash)
			assert.Equal(t, tt.want, got)
		})
	}
}
