package spec_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
)

// def builds a scalar XDR type definition.
func def(t xdr.ScSpecType) xdr.ScSpecTypeDef { return xdr.ScSpecTypeDef{Type: t} }

func vecOf(inner xdr.ScSpecTypeDef) xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeVec,
		Vec:  &xdr.ScSpecTypeVec{ElementType: inner},
	}
}

func optionOf(inner xdr.ScSpecTypeDef) xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{
		Type:   xdr.ScSpecTypeScSpecTypeOption,
		Option: &xdr.ScSpecTypeOption{ValueType: inner},
	}
}

func TestConvertTypeScalars(t *testing.T) {
	t.Parallel()

	// Every scalar the XDR defines must map to a kind, so that a contract
	// using an exotic primitive does not fail to decode as a whole.
	tests := []struct {
		in          xdr.ScSpecType
		wantKind    model.Kind
		wantDisplay string
	}{
		{xdr.ScSpecTypeScSpecTypeVal, model.KindVal, "Val"},
		{xdr.ScSpecTypeScSpecTypeBool, model.KindBool, "bool"},
		{xdr.ScSpecTypeScSpecTypeVoid, model.KindVoid, "()"},
		{xdr.ScSpecTypeScSpecTypeError, model.KindError, "Error"},
		{xdr.ScSpecTypeScSpecTypeU32, model.KindU32, "u32"},
		{xdr.ScSpecTypeScSpecTypeI32, model.KindI32, "i32"},
		{xdr.ScSpecTypeScSpecTypeU64, model.KindU64, "u64"},
		{xdr.ScSpecTypeScSpecTypeI64, model.KindI64, "i64"},
		{xdr.ScSpecTypeScSpecTypeTimepoint, model.KindTimepoint, "Timepoint"},
		{xdr.ScSpecTypeScSpecTypeDuration, model.KindDuration, "Duration"},
		{xdr.ScSpecTypeScSpecTypeU128, model.KindU128, "u128"},
		{xdr.ScSpecTypeScSpecTypeI128, model.KindI128, "i128"},
		{xdr.ScSpecTypeScSpecTypeU256, model.KindU256, "u256"},
		{xdr.ScSpecTypeScSpecTypeI256, model.KindI256, "i256"},
		{xdr.ScSpecTypeScSpecTypeBytes, model.KindBytes, "Bytes"},
		{xdr.ScSpecTypeScSpecTypeString, model.KindString, "String"},
		{xdr.ScSpecTypeScSpecTypeSymbol, model.KindSymbol, "Symbol"},
		{xdr.ScSpecTypeScSpecTypeAddress, model.KindAddress, "Address"},
		{xdr.ScSpecTypeScSpecTypeMuxedAddress, model.KindMuxedAddress, "MuxedAddress"},
	}

	for _, tt := range tests {
		t.Run(tt.wantDisplay, func(t *testing.T) {
			t.Parallel()

			got, err := spec.ConvertType(def(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, got.Kind)
			assert.Equal(t, tt.wantDisplay, got.Display)
		})
	}
}

func TestConvertTypeComposites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          xdr.ScSpecTypeDef
		wantKind    model.Kind
		wantDisplay string
		check       func(t *testing.T, got model.Type)
	}{
		{
			name:        "vec",
			in:          vecOf(def(xdr.ScSpecTypeScSpecTypeU32)),
			wantKind:    model.KindVec,
			wantDisplay: "Vec<u32>",
			check: func(t *testing.T, got model.Type) {
				require.NotNil(t, got.Element)
				assert.Equal(t, model.KindU32, got.Element.Kind)
			},
		},
		{
			name:        "option",
			in:          optionOf(def(xdr.ScSpecTypeScSpecTypeAddress)),
			wantKind:    model.KindOption,
			wantDisplay: "Option<Address>",
			check: func(t *testing.T, got model.Type) {
				require.NotNil(t, got.Inner)
				assert.Equal(t, model.KindAddress, got.Inner.Kind)
			},
		},
		{
			name: "map",
			in: xdr.ScSpecTypeDef{
				Type: xdr.ScSpecTypeScSpecTypeMap,
				Map: &xdr.ScSpecTypeMap{
					KeyType:   def(xdr.ScSpecTypeScSpecTypeSymbol),
					ValueType: def(xdr.ScSpecTypeScSpecTypeI128),
				},
			},
			wantKind:    model.KindMap,
			wantDisplay: "Map<Symbol, i128>",
			check: func(t *testing.T, got model.Type) {
				require.NotNil(t, got.Key)
				require.NotNil(t, got.Value)
				assert.Equal(t, model.KindSymbol, got.Key.Kind)
				assert.Equal(t, model.KindI128, got.Value.Kind)
			},
		},
		{
			name: "result",
			in: xdr.ScSpecTypeDef{
				Type: xdr.ScSpecTypeScSpecTypeResult,
				Result: &xdr.ScSpecTypeResult{
					OkType:    def(xdr.ScSpecTypeScSpecTypeVoid),
					ErrorType: def(xdr.ScSpecTypeScSpecTypeError),
				},
			},
			wantKind:    model.KindResult,
			wantDisplay: "Result<(), Error>",
			check: func(t *testing.T, got model.Type) {
				require.NotNil(t, got.Ok)
				require.NotNil(t, got.Error)
			},
		},
		{
			name: "tuple",
			in: xdr.ScSpecTypeDef{
				Type: xdr.ScSpecTypeScSpecTypeTuple,
				Tuple: &xdr.ScSpecTypeTuple{ValueTypes: []xdr.ScSpecTypeDef{
					def(xdr.ScSpecTypeScSpecTypeAddress),
					def(xdr.ScSpecTypeScSpecTypeU64),
				}},
			},
			wantKind:    model.KindTuple,
			wantDisplay: "(Address, u64)",
			check: func(t *testing.T, got model.Type) {
				assert.Len(t, got.Elements, 2)
			},
		},
		{
			name: "empty tuple",
			in: xdr.ScSpecTypeDef{
				Type:  xdr.ScSpecTypeScSpecTypeTuple,
				Tuple: &xdr.ScSpecTypeTuple{},
			},
			wantKind:    model.KindTuple,
			wantDisplay: "()",
			check:       func(t *testing.T, got model.Type) { assert.Empty(t, got.Elements) },
		},
		{
			name: "bytesN",
			in: xdr.ScSpecTypeDef{
				Type:   xdr.ScSpecTypeScSpecTypeBytesN,
				BytesN: &xdr.ScSpecTypeBytesN{N: 32},
			},
			wantKind:    model.KindBytesN,
			wantDisplay: "BytesN<32>",
			check:       func(t *testing.T, got model.Type) { assert.Equal(t, uint32(32), got.N) },
		},
		{
			name: "udt",
			in: xdr.ScSpecTypeDef{
				Type: xdr.ScSpecTypeScSpecTypeUdt,
				Udt:  &xdr.ScSpecTypeUdt{Name: "Balance"},
			},
			wantKind:    model.KindUDT,
			wantDisplay: "Balance",
			check:       func(t *testing.T, got model.Type) { assert.Equal(t, "Balance", got.Name) },
		},
		{
			name:        "nested containers",
			in:          optionOf(vecOf(optionOf(def(xdr.ScSpecTypeScSpecTypeU32)))),
			wantKind:    model.KindOption,
			wantDisplay: "Option<Vec<Option<u32>>>",
			check:       func(t *testing.T, got model.Type) { require.NotNil(t, got.Inner) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := spec.ConvertType(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, got.Kind)
			assert.Equal(t, tt.wantDisplay, got.Display)
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestConvertTypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      xdr.ScSpecTypeDef
		wantErr string
	}{
		{
			name:    "unknown type code",
			in:      xdr.ScSpecTypeDef{Type: xdr.ScSpecType(31337)},
			wantErr: "unknown spec type",
		},
		{
			name:    "vec without body",
			in:      xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeVec},
			wantErr: "vec type has no body",
		},
		{
			name:    "map without body",
			in:      xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeMap},
			wantErr: "map type has no body",
		},
		{
			name:    "udt without body",
			in:      xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt},
			wantErr: "udt type has no body",
		},
		{
			name:    "error is reported with its path",
			in:      vecOf(xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeOption}),
			wantErr: "vec element: option type has no body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := spec.ConvertType(tt.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestConvertTypeDepthLimit checks that a pathologically nested type is
// rejected rather than exhausting the stack.
func TestConvertTypeDepthLimit(t *testing.T) {
	t.Parallel()

	nested := def(xdr.ScSpecTypeScSpecTypeU32)
	for range 200 {
		nested = vecOf(nested)
	}

	_, err := spec.ConvertType(nested)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested deeper than")
}
