package spec_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/spec"
)

// TestReadEntriesEdgeCases adds table-driven edge cases that the existing
// TestReadEntries / TestReadEntriesErrors tests do not reach: empty input, the
// size-limit boundary, and a corrupt single-byte section.
func TestReadEntriesEdgeCases(t *testing.T) {
	t.Parallel()

	singleEntry := encodeEntries(t, functionEntry("edge", nil, nil))
	overSize := make([]byte, spec.MaxSpecSectionSize+1)

	tests := []struct {
		name    string
		section []byte
		wantN   int
		wantErr bool
		errIs   error
	}{
		{"nil input yields empty slice", nil, 0, false, nil},
		{"single valid entry round-trips", singleEntry, 1, false, nil},
		{"section one byte over size limit", overSize, 0, true, spec.ErrLimitExceeded},
		{"single zero byte is invalid XDR", []byte{0x00}, 0, true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries, err := spec.ReadEntries(tc.section)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errIs != nil {
					assert.ErrorIs(t, err, tc.errIs)
				}
			} else {
				require.NoError(t, err)
				assert.Len(t, entries, tc.wantN)
			}
		})
	}
}

// TestFromEntriesEdgeCases adds table-driven edge cases for FromEntries:
// empty input, an entry with a nil body, and a slice exceeding MaxSpecEntries.
func TestFromEntriesEdgeCases(t *testing.T) {
	t.Parallel()

	nilBodyEntry := xdr.ScSpecEntry{
		Kind:       xdr.ScSpecEntryKindScSpecEntryFunctionV0,
		FunctionV0: nil,
	}
	overLimit := make([]xdr.ScSpecEntry, spec.MaxSpecEntries+1)

	tests := []struct {
		name    string
		entries []xdr.ScSpecEntry
		wantErr bool
		errIs   error
	}{
		{"nil entries produce an empty interface", nil, false, nil},
		{"function entry with nil body is an error", []xdr.ScSpecEntry{nilBodyEntry}, true, nil},
		{"entries over MaxSpecEntries", overLimit, true, spec.ErrLimitExceeded},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			iface, err := spec.FromEntries(tc.entries)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errIs != nil {
					assert.ErrorIs(t, err, tc.errIs)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, iface)
			}
		})
	}
}
