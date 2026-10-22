package stellar_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar"
)

// instanceEntryData builds the ledger entry data of a contract instance
// entry pointing at wasmHash, so tests can corrupt it in controlled ways.
func instanceEntryData(t *testing.T, contractID string, wasmHash xdr.Hash) xdr.LedgerEntryData {
	t.Helper()

	instance := xdr.ScContractInstance{
		Executable: xdr.ContractExecutable{
			Type:     xdr.ContractExecutableTypeContractExecutableWasm,
			WasmHash: &wasmHash,
		},
	}
	return xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   contractAddress(t, contractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        xdr.ScVal{Type: xdr.ScValTypeScvContractInstance, Instance: &instance},
		},
	}
}

// codeEntryData builds the ledger entry data of a contract code entry
// holding the given module.
func codeEntryData(t *testing.T, wasmHash xdr.Hash, code []byte) xdr.LedgerEntryData {
	t.Helper()

	return xdr.LedgerEntryData{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.ContractCodeEntry{Hash: wasmHash, Code: code},
	}
}

// codeLedgerKey returns the encoded ledger key the network stores a code
// entry under.
func codeLedgerKey(t *testing.T, wasmHash xdr.Hash) string {
	t.Helper()

	var key xdr.LedgerKey
	require.NoError(t, key.SetContractCode(wasmHash))
	encoded, err := key.MarshalBinaryBase64()
	require.NoError(t, err)
	return encoded
}

// TestMalformedInstanceEntry covers the decode failures of the instance
// entry: invalid base64, truncated XDR and an empty payload. Each error must
// name what was being decoded and for which contract, because the entry came
// from a remote endpoint and "invalid argument" alone leaves an operator
// nothing to act on.
func TestMalformedInstanceEntry(t *testing.T) {
	t.Parallel()

	valid, err := instanceEntryData(t, testContractID, hashOf(0x55)).MarshalBinary()
	require.NoError(t, err)

	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "invalid base64",
			data: "not base64!!",
			want: []string{"decoding instance entry for " + testContractID, "base64"},
		},
		{
			name: "truncated xdr",
			data: base64.StdEncoding.EncodeToString(valid[:len(valid)/2]),
			want: []string{"decoding instance entry for " + testContractID},
		},
		{
			name: "empty payload",
			data: "",
			want: []string{"decoding instance entry for " + testContractID, "empty xdr payload"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeRPC(t)
			srv := fake.start()
			fake.entries[instanceKey(t, testContractID)] = tt.data

			f := stellar.NewRPCFetcher(srv.URL, srv.Client())
			t.Cleanup(func() { _ = f.Close() })

			_, err := f.FetchContract(context.Background(), testContractID)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want,
					"the error must say what was being decoded so the offending entry is findable")
			}
		})
	}
}

// TestMalformedCodeEntry covers the same decode failures on the code entry,
// reached both directly by hash and through a contract instance.
func TestMalformedCodeEntry(t *testing.T) {
	t.Parallel()

	wasmHash := hashOf(0x66)
	wasmHashHex := hex.EncodeToString(wasmHash[:])

	valid, err := codeEntryData(t, wasmHash, []byte("\x00asm\x01\x00\x00\x00 pretend module")).MarshalBinary()
	require.NoError(t, err)

	tests := []struct {
		name string
		data string
	}{
		{"invalid base64", "not base64!!"},
		{"truncated xdr", base64.StdEncoding.EncodeToString(valid[:len(valid)/2])},
		{"empty payload", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeRPC(t)
			srv := fake.start()
			fake.putInstance(testContractID, wasmHash)
			fake.entries[codeLedgerKey(t, wasmHash)] = tt.data

			f := stellar.NewRPCFetcher(srv.URL, srv.Client())
			t.Cleanup(func() { _ = f.Close() })

			// The error must name the entry by its hash: it is the only
			// handle on an entry the network returned but we could not read.
			_, err := f.FetchWasm(context.Background(), wasmHashHex)
			assertDecodeCodeEntryError(t, err, wasmHashHex)
			assertDecodeCodeEntryError(t, fetchContractError(t, f), wasmHashHex)
		})
	}
}

func assertDecodeCodeEntryError(t *testing.T, err error, wasmHashHex string) {
	t.Helper()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding code entry",
		"the error must say what was being decoded")
	assert.Contains(t, err.Error(), wasmHashHex,
		"the error must name the wasm hash whose entry failed to decode")
}

// fetchContractError fetches through the instance and returns the error, so
// both routes to the code entry can be asserted with one helper.
func fetchContractError(t *testing.T, f *stellar.RPCFetcher) error {
	t.Helper()

	_, err := f.FetchContract(context.Background(), testContractID)
	return err
}

// TestMissingCodeEntryIsWasmNotFound proves the not-found case is
// distinguishable from every other failure: callers decide whether to retry
// later (the entry may be restored) based on this sentinel.
func TestMissingCodeEntryIsWasmNotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeRPC(t)
	srv := fake.start()

	wasmHash := hashOf(0x77)
	wasmHashHex := hex.EncodeToString(wasmHash[:])
	// The instance exists and points at a code entry that has expired.
	fake.putInstance(testContractID, wasmHash)

	f := stellar.NewRPCFetcher(srv.URL, srv.Client())
	t.Cleanup(func() { _ = f.Close() })

	_, err := f.FetchContract(context.Background(), testContractID)
	require.ErrorIs(t, err, stellar.ErrWasmNotFound,
		"missing code must be distinguishable from a missing contract")
	assert.Contains(t, err.Error(), "no code entry")

	// The same sentinel when the hash is requested directly, with no
	// contract instance involved.
	_, err = f.FetchWasm(context.Background(), wasmHashHex)
	require.ErrorIs(t, err, stellar.ErrWasmNotFound)
}

// TestWrongEntryKindIsRejected covers an entry that decodes fine but is not
// the kind of entry that was asked for — the shape a misbehaving endpoint,
// or a ledger key swap, would produce.
func TestWrongEntryKindIsRejected(t *testing.T) {
	t.Parallel()

	wasmHash := hashOf(0x88)
	code := []byte("\x00asm\x01\x00\x00\x00 pretend module")

	tests := []struct {
		name    string
		setup   func(t *testing.T, fake *fakeRPC)
		fetch   func(t *testing.T, f *stellar.RPCFetcher) error
		wantSub string
	}{
		{
			name: "code request answered with instance entry",
			setup: func(t *testing.T, fake *fakeRPC) {
				fake.entries[codeLedgerKey(t, wasmHash)] = marshalBase64(t, instanceEntryData(t, testContractID, wasmHash))
			},
			fetch: func(t *testing.T, f *stellar.RPCFetcher) error {
				_, err := f.FetchWasm(context.Background(), hex.EncodeToString(wasmHash[:]))
				return err
			},
			wantSub: "is not contract code",
		},
		{
			name: "instance request answered with code entry",
			setup: func(t *testing.T, fake *fakeRPC) {
				fake.entries[instanceKey(t, testContractID)] = marshalBase64(t, codeEntryData(t, wasmHash, code))
			},
			fetch: func(t *testing.T, f *stellar.RPCFetcher) error {
				_, err := f.FetchContract(context.Background(), testContractID)
				return err
			},
			wantSub: "is not contract data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeRPC(t)
			srv := fake.start()
			tt.setup(t, fake)

			f := stellar.NewRPCFetcher(srv.URL, srv.Client())
			t.Cleanup(func() { _ = f.Close() })

			err := tt.fetch(t, f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

// TestInstanceEntryWithoutInstance covers a contract data entry that holds
// no instance — e.g. a TTL entry for the contract — answering the instance
// lookup. The fetcher must say so rather than report a decode failure.
func TestInstanceEntryWithoutInstance(t *testing.T) {
	t.Parallel()

	fake := newFakeRPC(t)
	srv := fake.start()

	one := xdr.Uint64(1)
	data := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   contractAddress(t, testContractID),
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &one},
		},
	}
	fake.entries[instanceKey(t, testContractID)] = marshalBase64(t, data)

	f := stellar.NewRPCFetcher(srv.URL, srv.Client())
	t.Cleanup(func() { _ = f.Close() })

	_, err := f.FetchContract(context.Background(), testContractID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds no contract instance")
	assert.Contains(t, err.Error(), testContractID)
}
