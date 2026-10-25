package spec_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
)

func voidUnionCase(name, doc string) xdr.ScSpecUdtUnionCaseV0 {
	return xdr.ScSpecUdtUnionCaseV0{
		Kind:     xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0,
		VoidCase: &xdr.ScSpecUdtUnionCaseVoidV0{Name: name, Doc: doc},
	}
}

func tupleUnionCase(name, doc string, types ...xdr.ScSpecTypeDef) xdr.ScSpecUdtUnionCaseV0 {
	return xdr.ScSpecUdtUnionCaseV0{
		Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0,
		TupleCase: &xdr.ScSpecUdtUnionCaseTupleV0{
			Name: name,
			Doc:  doc,
			Type: types,
		},
	}
}

func unionWithCases(name string, cases ...xdr.ScSpecUdtUnionCaseV0) xdr.ScSpecEntry {
	return xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtUnionV0,
		UdtUnionV0: &xdr.ScSpecUdtUnionV0{
			Name:  name,
			Cases: cases,
		},
	}
}

// TestUnionCaseShapes covers the three shapes a union case can take: no
// payload, one payload value, and several payload values. Each is encoded by
// hand and decoded through a real WASM module, so the checks run over the
// same path a contract fetched from the network takes.
func TestUnionCaseShapes(t *testing.T) {
	t.Parallel()

	iface := decodeSection(t, unionWithCases("DataKey",
		voidUnionCase("Admin", "the administrator"),
		tupleUnionCase("Amount", "a single amount", def(xdr.ScSpecTypeScSpecTypeI128)),
		tupleUnionCase("Transfer", "who moved how much",
			def(xdr.ScSpecTypeScSpecTypeAddress),
			def(xdr.ScSpecTypeScSpecTypeI128),
			def(xdr.ScSpecTypeScSpecTypeSymbol),
		),
	))

	require.Len(t, iface.Types.Unions, 1)
	union := iface.Types.Unions[0]
	assert.Equal(t, "DataKey", union.Name)

	require.Len(t, union.Cases, 3)

	admin := union.Cases[0]
	assert.Equal(t, "Admin", admin.Name)
	assert.Equal(t, "the administrator", admin.Doc)
	assert.Empty(t, admin.Values, "a void case carries no payload")
	assert.NotNil(t, admin.Values, "the payload is an empty slice, not nil")

	amount := union.Cases[1]
	assert.Equal(t, "Amount", amount.Name)
	require.Len(t, amount.Values, 1)
	assert.Equal(t, model.KindI128, amount.Values[0].Kind)
	assert.Equal(t, "i128", amount.Values[0].Display)

	transfer := union.Cases[2]
	assert.Equal(t, "Transfer", transfer.Name)
	require.Len(t, transfer.Values, 3)
	assert.Equal(t, []string{"Address", "i128", "Symbol"}, unionCaseDisplays(transfer))
}

// TestUnionCaseReferencesUDT pins that a union case payload referring to a
// user-defined type keeps the reference by name instead of embedding the
// definition. The struct is declared alongside the union so the test also
// proves the name resolves to a type in the same interface.
func TestUnionCaseReferencesUDT(t *testing.T) {
	t.Parallel()

	balance := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Balance"},
	}

	iface := decodeSection(t,
		xdr.ScSpecEntry{
			Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
			UdtStructV0: &xdr.ScSpecUdtStructV0{
				Name: "Balance",
				Fields: []xdr.ScSpecUdtStructFieldV0{
					{Name: "amount", Type: def(xdr.ScSpecTypeScSpecTypeI128)},
				},
			},
		},
		unionWithCases("Account",
			tupleUnionCase("Holds", "", balance),
		),
	)

	require.Len(t, iface.Types.Structs, 1)
	require.Len(t, iface.Types.Unions, 1)

	require.Len(t, iface.Types.Unions[0].Cases, 1)
	values := iface.Types.Unions[0].Cases[0].Values
	require.Len(t, values, 1)

	ref := values[0]
	assert.Equal(t, model.KindUDT, ref.Kind)
	assert.Equal(t, "Balance", ref.Name)
	assert.Equal(t, "Balance", ref.Display)
	assert.Equal(t, iface.Types.Structs[0].Name, ref.Name,
		"the union must reference the declared struct by name")
}

// TestUnionErrors covers every way convertUnion can fail, including the
// unknown case kind whose message has to name the offending case index.
//
// These are built as entries rather than modules: the SDK's XDR codec
// rejects a malformed union while marshalling it, so a section cannot carry
// these shapes and the conversion has to be driven directly.
func TestUnionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entry   xdr.ScSpecEntry
		wantErr []string
	}{
		{
			name:    "union without a body",
			entry:   xdr.ScSpecEntry{Kind: xdr.ScSpecEntryKindScSpecEntryUdtUnionV0},
			wantErr: []string{"union entry has no body"},
		},
		{
			name: "void case without a body",
			entry: unionWithCases("U", xdr.ScSpecUdtUnionCaseV0{
				Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0,
			}),
			wantErr: []string{"case 0", "void case has no body"},
		},
		{
			name: "tuple case without a body",
			entry: unionWithCases("U", xdr.ScSpecUdtUnionCaseV0{
				Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0,
			}),
			wantErr: []string{"case 0", "tuple case has no body"},
		},
		{
			name: "tuple case value fails to convert",
			entry: unionWithCases("U", tupleUnionCase("Broken", "",
				xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt},
			)),
			wantErr: []string{`case "Broken" value 0`, "udt type has no body"},
		},
		{
			name: "unknown case kind names the case index",
			entry: unionWithCases("U",
				voidUnionCase("Ok", ""),
				xdr.ScSpecUdtUnionCaseV0{Kind: xdr.ScSpecUdtUnionCaseV0Kind(42)},
			),
			wantErr: []string{"case 1", "unknown union case kind 42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := spec.FromEntries([]xdr.ScSpecEntry{tt.entry})
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func unionCaseDisplays(c model.UnionCase) []string {
	out := make([]string, len(c.Values))
	for i := range c.Values {
		out[i] = c.Values[i].Display
	}
	return out
}
