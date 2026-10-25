package spec_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// metaSection builds a "contractmetav0" section body from hand-encoded
// SCMetaEntry values, laid out back to back with no framing between them.
func metaSection(t *testing.T, entries ...xdr.ScMetaEntry) []byte {
	t.Helper()

	var buf []byte
	for i := range entries {
		raw, err := entries[i].MarshalBinary()
		require.NoError(t, err)
		buf = append(buf, raw...)
	}
	return buf
}

func metaV0(key, val string) xdr.ScMetaEntry {
	return xdr.ScMetaEntry{
		Kind: xdr.ScMetaKindScMetaV0,
		V0:   &xdr.ScMetaV0{Key: key, Val: val},
	}
}

// decodeMetaModule builds a module with a trivial spec section and the given
// metadata section body, decodes it, and returns the interface. Metadata
// problems must not fail the decode, so the test asserts no error.
func decodeMetaModule(t *testing.T, metaBody []byte) *model.Interface {
	t.Helper()

	module := wasmtest.Module(
		wasmtest.Section{Name: wasm.SectionContractSpec, Body: encodeEntries(t, functionEntry("hello", nil, nil))},
		wasmtest.Section{Name: wasm.SectionContractMeta, Body: metaBody},
	)

	iface, err := spec.XDRDecoder{}.Decode(module)
	require.NoError(t, err)
	require.NotNil(t, iface)
	return iface
}

func TestDecodeMetaValid(t *testing.T) {
	t.Parallel()

	iface := decodeMetaModule(t, metaSection(t,
		metaV0("rsver", "1.93.0"),
		metaV0("rssdkver", "25.3.0"),
	))

	// The spec section still decodes, so the interface is usable.
	require.Len(t, iface.Functions, 1)
	assert.Equal(t, "hello", iface.Functions[0].Name)

	// Metadata survives decoding.
	assert.Equal(t, map[string]string{
		"rsver":    "1.93.0",
		"rssdkver": "25.3.0",
	}, iface.Meta)
}

func TestDecodeMetaTruncated(t *testing.T) {
	t.Parallel()

	// A truncated first entry: the union discriminant itself cannot be read,
	// so decodeMeta stops (io.ErrUnexpectedEOF -> break) and reports no meta.
	// The metadata problem must not make the otherwise valid interface
	// unusable.
	full := metaSection(t, metaV0("rsver", "1.93.0"))
	iface := decodeMetaModule(t, full[:2])

	assert.Nil(t, iface.Meta, "a truncated section yields no meta")
	require.Len(t, iface.Functions, 1)
	assert.Equal(t, "hello", iface.Functions[0].Name)
}

func TestDecodeMetaTruncatedKeepsValidPrefix(t *testing.T) {
	t.Parallel()

	// One complete entry followed by the start of another that can never be
	// finished: decodeMeta keeps the first entry and stops at the truncation,
	// so the valid metadata survives and the interface stays usable.
	first := metaSection(t, metaV0("rsver", "1.93.0"))
	iface := decodeMetaModule(t, append(append([]byte{}, first...), 0))

	assert.Equal(t, map[string]string{"rsver": "1.93.0"}, iface.Meta)
	require.Len(t, iface.Functions, 1)
}

func TestDecodeMetaEmpty(t *testing.T) {
	t.Parallel()

	// A metadata section with no entries yields no meta field.
	iface := decodeMetaModule(t, nil)

	assert.Nil(t, iface.Meta, "an empty section yields no meta")
	require.Len(t, iface.Functions, 1)
}

func TestDecodeMetaDuplicateKeys(t *testing.T) {
	t.Parallel()

	// The same key written twice: the last value wins, deterministically,
	// so decoding is stable regardless of map iteration order.
	iface := decodeMetaModule(t, metaSection(t,
		metaV0("rsver", "1.0.0"),
		metaV0("rsver", "2.0.0"),
	))

	assert.Equal(t, map[string]string{"rsver": "2.0.0"}, iface.Meta)
}
