package codegen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// TestGoldenPythonClient generates the Python client for the real testnet
// contract whose decoded ABI the spec package pins, and compares it with a
// checked-in file. The codegen-pytypecheck CI job compiles that file and
// imports it against stellar-sdk, so the golden is known to load, not
// merely to be stable.
func TestGoldenPythonClient(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	got, err := codegen.Python(&iface, src)
	require.NoError(t, err)

	golden := filepath.Join("testdata", "zkvote.client.py")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("wrote %s", golden)
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test-owned golden path
	require.NoError(t, err, "golden file missing; regenerate with -update")
	assert.Equal(t, string(want), got, "generated client changed; if intended, rerun with -update")
}

func TestPythonTypeMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   model.Type
		want string
	}{
		{model.Scalar(model.KindU32), "int"},
		{model.Scalar(model.KindI128), "int"},
		{model.Scalar(model.KindTimepoint), "int"},
		{model.Scalar(model.KindBool), "bool"},
		{model.Scalar(model.KindVoid), "None"},
		{model.Scalar(model.KindError), "int"},
		{model.Scalar(model.KindAddress), "str"},
		{model.Scalar(model.KindMuxedAddress), "str"},
		{model.Scalar(model.KindSymbol), "str"},
		{model.Scalar(model.KindBytes), "bytes"},
		{model.Type{Kind: model.KindBytesN, N: 32}, "bytes"},
		{model.Type{Kind: model.KindOption, Inner: ptr(model.Scalar(model.KindU64))}, "Optional[int]"},
		{model.Type{Kind: model.KindVec, Element: ptr(model.Scalar(model.KindString))}, "List[str]"},
		{model.Type{Kind: model.KindMap, Key: ptr(model.Scalar(model.KindSymbol)), Value: ptr(model.Scalar(model.KindI128))}, "Dict[str, int]"},
		{model.Type{Kind: model.KindTuple, Elements: []model.Type{model.Scalar(model.KindU32), model.Scalar(model.KindBool)}}, "Tuple[int, bool]"},
		{model.Type{Kind: model.KindUDT, Name: "Proposal"}, "Proposal"},
	}

	for _, tt := range tests {
		iface := &model.Interface{Functions: []model.Function{
			{Name: "f", Inputs: []model.Param{{Name: "x", Type: tt.in}}},
		}}
		got, err := codegen.Python(iface, src)
		require.NoError(t, err, tt.want)
		assert.Contains(t, got, "def f(self, x: "+tt.want+", **kwargs: Any)", tt.want)
	}
}

// TestPythonErrorEnumReferenceIsItsCode checks that a value of an error
// enum is encoded as SCV_ERROR with the numeric code, which is what
// SEP-48 says a contract expects.
func TestPythonErrorEnumReferenceIsItsCode(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Types: model.Types{ErrorEnums: []model.ErrorEnum{{
			Name:  "ContractError",
			Cases: []model.EnumCase{{Name: "Unauthorized", Value: 4}},
		}}},
		Functions: []model.Function{{
			Name:   "f",
			Inputs: []model.Param{{Name: "e", Type: model.Type{Kind: model.KindUDT, Name: "ContractError"}}},
		}},
	}
	got, err := codegen.Python(iface, src)
	require.NoError(t, err)
	assert.Contains(t, got, "def f(self, e: int, **kwargs: Any)")
	assert.Contains(t, got, "_error(e),")
	assert.Contains(t, got, "ERROR_CODES: Dict[int, str] = {")
	assert.Contains(t, got, `4: "Unauthorized",`)
}

// TestPythonUnsupportedTypeFails checks that an interface the Python
// client cannot carry is reported as an error rather than emitted as
// broken source.
func TestPythonUnsupportedTypeFails(t *testing.T) {
	t.Parallel()

	// A Result only appears as a return value, which the caller decodes
	// from a simulation; as a value it has no Python representation.
	iface := &model.Interface{Functions: []model.Function{{
		Name: "f",
		Inputs: []model.Param{{Name: "r", Type: model.Type{
			Kind:  model.KindResult,
			Ok:    ptr(model.Scalar(model.KindVoid)),
			Error: ptr(model.Type{Kind: model.KindUDT, Name: "Error"}),
		}}},
	}}}
	_, err := codegen.Python(iface, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Result")
}

// TestPythonDeclaresContractTypes checks the shapes a contract's
// user-defined types take: a struct as a dataclass with a map encoder, a
// union as a tagged dataclass with a constructor per case, and an enum as
// an IntEnum with a member per case.
func TestPythonDeclaresContractTypes(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Types: model.Types{
			Structs: []model.Struct{{
				Name:   "Point",
				Fields: []model.StructField{{Name: "x", Type: model.Scalar(model.KindI32)}, {Name: "y", Type: model.Scalar(model.KindI32)}},
			}},
			Unions: []model.Union{{
				Name: "Move",
				Cases: []model.UnionCase{
					{Name: "Up"},
					{Name: "To", Values: []model.Type{model.Scalar(model.KindI32)}},
				},
			}},
			Enums: []model.Enum{{
				Name:  "Status",
				Cases: []model.EnumCase{{Name: "Open", Value: 0}, {Name: "Closed", Value: 1}},
			}},
		},
		Functions: []model.Function{{
			Name:   "apply",
			Inputs: []model.Param{{Name: "p", Type: model.Type{Kind: model.KindUDT, Name: "Point"}}},
		}},
	}
	got, err := codegen.Python(iface, src)
	require.NoError(t, err)

	assert.Contains(t, got, "class Point:")
	assert.Contains(t, got, `return scval.to_struct({`)
	assert.Contains(t, got, `"x": scval.to_int32(self.x),`)

	assert.Contains(t, got, "class Move:")
	assert.Contains(t, got, "values: List[stellar_xdr.SCVal]")
	assert.Contains(t, got, "return scval.to_vec([scval.to_symbol(self.tag), *self.values])")
	assert.Contains(t, got, "def move_up() -> Move:")
	assert.Contains(t, got, "def move_to(value0: int) -> Move:")

	assert.Contains(t, got, "class Status(IntEnum):")
	assert.Regexp(t, `(?m)^    OPEN = 0$`, got)
	assert.Regexp(t, `(?m)^    CLOSED = 1$`, got)
}
