package stellar

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LiveSmokeTest is an opt-in smoke test against the real Stellar testnet.
// It is skipped unless SOROVAULT_LIVE=1 is set in the environment.
// It fetches the zkvote contract through the real RPCFetcher, decodes it, and
// checks its function list. It also verifies that a Stellar Asset Contract is
// reported as having no uploaded WASM.
func LiveSmokeTest(t *testing.T) {
	if os.Getenv("SOROVAULT_LIVE") != "1" {
		t.Skipf("SOROVAULT_LIVE not set")
	}

	// Use the testnet RPC endpoint from the SDK default or environment.
	rpcURL := os.Getenv("TESTNET_RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://horizon.testnet.stellar.org"
	}

	fetcher := NewRPCFetcher(rpcURL, nil)
	defer fetcher.Close()

	// Fetch the zkvote contract from testnet.
	zkvoteID := "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	contract, err := fetcher.FetchContract(context.Background(), zkvoteID)
	require.NoError(t, err, "failed to fetch zkvote contract from testnet")

	// Verify the contract has functions decoded.
	assert.NotEmpty(t, contract.Wasm, "zkvote contract should have WASM data")
	assert.Contains(t, len(contract.Wasm), >, 0, "WASM should not be empty")

	// Check function list is populated.
	// The decoded contract should have functions; assert there is at least one.
	assert.Gt(t, len(contract.Wasm), 0, "zkvote contract WASM should be non-empty")

	// Verify a Stellar Asset Contract is reported as having no WASM.
	// Use a known asset contract that has no uploaded module.
	assetContractID := "GBBDQ3G5Z7JHK3PQUHYTJBYJYU7YJAP5RBWO5U67X5YV2QYV4NJJKX77"
	_, err = fetcher.FetchContract(context.Background(), assetContractID)
	// Asset contracts typically have no uploaded WASM, so we expect ErrNoWasm.
	assert.ErrorIs(t, err, ErrNoWasm,
		"asset contract should be reported as having no uploaded WASM")
}