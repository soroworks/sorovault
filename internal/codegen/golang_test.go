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

// TestGoldenGoClient generates the Go client for the real testnet contract
// whose decoded ABI the spec package pins, and compares it with a
// checked-in file. The codegen-gotypecheck CI job compiles that file
// against github.com/stellar/go-stellar-sdk, so the golden is known to
// build, not merely to be stable.
func TestGoldenGoClient(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../spec/testdata/zkvote.golden.json")
	require.NoError(t, err)
	var iface model.Interface
	require.NoError(t, json.Unmarshal(raw, &iface))

	got, err := codegen.Go(&iface, src, "zkvote")
	require.NoError(t, err)

	golden := filepath.Join("testdata", "zkvote.client.go")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("wrote %s", golden)
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test-owned golden path
	require.NoError(t, err, "golden file missing; regenerate with -update")
	assert.Equal(t, string(want), got, "generated client changed; if intended, rerun with -update")
}

func TestGoTypeMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   model.Type
		want string
	}{
		{model.Scalar(model.KindU32), "uint32"},
		{model.Scalar(model.KindI32), "int32"},
		{model.Scalar(model.KindU64), "uint64"},
		{model.Scalar(model.KindI128), "*big.Int"},
		{model.Scalar(model.KindU256), "*big.Int"},
		{model.Scalar(model.KindTimepoint), "uint64"},
		{model.Scalar(model.KindBool), "bool"},
		{model.Scalar(model.KindVoid), "struct{}"},
		{model.Scalar(model.KindError), "uint32"},
		{model.Scalar(model.KindAddress), "string"},
		{model.Scalar(model.KindMuxedAddress), "string"},
		{model.Scalar(model.KindSymbol), "string"},
		{model.Scalar(model.KindBytes), "[]byte"},
		{model.Type{Kind: model.KindBytesN, N: 32}, "[32]byte"},
		{model.Type{Kind: model.KindOption, Inner: ptr(model.Scalar(model.KindU64))}, "*uint64"},
		{model.Type{Kind: model.KindVec, Element: ptr(model.Scalar(model.KindString))}, "[]string"},
		{model.Type{Kind: model.KindMap, Key: ptr(model.Scalar(model.KindSymbol)), Value: ptr(model.Scalar(model.KindI128))}, "map[string]*big.Int"},
		{model.Type{Kind: model.KindUDT, Name: "Proposal"}, "Proposal"},
	}

	for _, tt := range tests {
		iface := &model.Interface{Functions: []model.Function{
			{Name: "f", Inputs: []model.Param{{Name: "x", Type: tt.in}}},
		}}
		got, err := codegen.Go(iface, src, "m")
		require.NoError(t, err, tt.want)
		assert.Contains(t, got, "func (client *Client) F(x "+tt.want+")", tt.want)
	}
}

// TestGoErrorEnumReferenceIsItsCode checks that a value of an error enum
// is encoded as its numeric code rather than as a symbol or a map, which
// is what SEP-48 says a contract expects.
func TestGoErrorEnumReferenceIsItsCode(t *testing.T) {
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
	got, err := codegen.Go(iface, src, "m")
	require.NoError(t, err)
	assert.Contains(t, got, "func (client *Client) F(e uint32)")
	assert.Contains(t, got, "return scValError(e)")
	assert.Contains(t, got, "var ErrorCodes = map[uint32]string{")
	assert.Contains(t, got, "4: \"Unauthorized\",")
}

// TestGoUnsupportedTypeFails checks that an interface the Go client cannot
// carry is reported as an error rather than emitted as broken source.
func TestGoUnsupportedTypeFails(t *testing.T) {
	t.Parallel()

	// A Result only appears as a return value, which the caller decodes
	// from a simulation; as a value it has no Go representation.
	iface := &model.Interface{Functions: []model.Function{{
		Name: "f",
		Inputs: []model.Param{{Name: "r", Type: model.Type{
			Kind:  model.KindResult,
			Ok:    ptr(model.Scalar(model.KindVoid)),
			Error: ptr(model.Type{Kind: model.KindUDT, Name: "Error"}),
		}}},
	}}}
	_, err := codegen.Go(iface, src, "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Result")
}

// TestGoDeclaresContractTypes checks the shapes a contract's user-defined
// types take: a struct as an exported-field struct with a map encoder, a
// union as a tagged struct with a constructor per case, and an enum as a
// named uint32 with a constant per case.
func TestGoDeclaresContractTypes(t *testing.T) {
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
	got, err := codegen.Go(iface, src, "m")
	require.NoError(t, err)

	assert.Contains(t, got, "type Point struct {\n\tX int32\n\tY int32\n}")
	assert.Contains(t, got, "entries = append(entries, xdr.ScMapEntry{Key: key, Val: value})")

	assert.Contains(t, got, "type Move struct {")
	assert.Contains(t, got, "\tTag string\n")
	assert.Contains(t, got, "\tValues []xdr.ScVal\n}")
	assert.Contains(t, got, "func MoveUp() Move {")
	assert.Contains(t, got, "func MoveTo(value0 int32) (Move, error) {")

	assert.Contains(t, got, "type Status uint32")
	assert.Regexp(t, `StatusOpen\s+Status = 0`, got)
	assert.Regexp(t, `StatusClosed\s+Status = 1`, got)
}

// TestNewClient covers NewClient generation and invocation methods for the Go client.
func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		contractID string
		network    string
		wantError  bool
	}{
		{
			name:       "happy path valid contract id",
			contractID: "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
			network:    "testnet",
			wantError:  false,
		},
		{
			name:       "error path invalid contract id",
			contractID: "INVALID_CONTRACT_ID",
			network:    "testnet",
			wantError:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			iface := &model.Interface{
				Functions: []model.Function{
					{Name: "ping"},
				},
			}
			got, err := codegen.Go(iface, src, "zkvote")
			require.NoError(t, err)

			assert.Contains(t, got, "func NewClient(contractID, network string) *Client")
			assert.Contains(t, got, "func (client *Client) Ping()")
		})
	}
}

// TestGoNoArgFunction covers functions that take no inputs, such as
// get_total_votes.  The generated method must call invoke with only the
// function name and produce no argument-encoding loop.
func TestGoNoArgFunction(t *testing.T) {
	t.Parallel()

	iface := &model.Interface{
		Functions: []model.Function{
			{
				Name:    "get_total_votes",
				Inputs:  nil,
				Outputs: []model.Type{model.Scalar(model.KindU64)},
			},
		},
	}

	got, err := codegen.Go(iface, src, "zkvote")
	require.NoError(t, err)

	// The generated method must exist and delegate directly to invoke with no
	// extra args -- no arg-encoding loop should appear.
	assert.Contains(t, got, "func (client *Client) GetTotalVotes() (txnbuild.Operation, error) {")
	assert.Contains(t, got, `return client.invoke("get_total_votes")`)
	assert.NotContains(t, got, `get_total_votes:`, "a no-arg function must not emit an error-wrapping fmt.Errorf line")
}
