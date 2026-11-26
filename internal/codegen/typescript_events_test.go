package codegen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/model"
)

// eventInterface is an interface declaring only the given events and
// structs, enough for the generator to emit their declarations.
func eventInterface(events []model.Event, structs ...model.Struct) *model.Interface {
	return &model.Interface{
		ABIVersion: model.ABIVersion,
		Types:      model.Types{Structs: structs, ErrorEnums: []model.ErrorEnum{}},
		Events:     events,
	}
}

func topic(name string, t model.Type) model.EventParam {
	return model.EventParam{Name: name, Type: t, Location: "topic-list"}
}

func data(name string, t model.Type) model.EventParam {
	return model.EventParam{Name: name, Type: t, Location: "data"}
}

// TestEventDataFormats proves each data format declares the payload the
// SDK decodes it into: an object for map, a tuple for vec, the bare value
// for single-value, and unknown for a format the generator does not know.
func TestEventDataFormats(t *testing.T) {
	t.Parallel()

	address := model.Scalar(model.KindAddress)
	i128 := model.Scalar(model.KindI128)
	u32 := model.Scalar(model.KindU32)

	tests := []struct {
		name   string
		format string
		params []model.EventParam
		want   []string
	}{
		{
			name:   "map is an object keyed by parameter name",
			format: "map",
			params: []model.EventParam{topic("from", address), data("amount", i128), data("memo", u32)},
			want: []string{
				"/** Data payload of the Transfer event: a map keyed by parameter name. */\nexport interface TransferEventData {\n  amount: sdk.i128;\n  memo: sdk.u32;\n}\n",
			},
		},
		{
			name:   "map with no data parameters is an empty object",
			format: "map",
			params: []model.EventParam{topic("from", address)},
			want:   []string{"export type TransferEventData = Record<string, never>;\n"},
		},
		{
			name:   "vec is a labelled tuple in parameter order",
			format: "vec",
			params: []model.EventParam{data("amount", i128), topic("from", address), data("memo", u32)},
			want: []string{
				"/** Data payload of the Transfer event: a vector of the parameters, in this order. */\nexport type TransferEventData = readonly [amount: sdk.i128, memo: sdk.u32];\n",
			},
		},
		{
			name:   "vec with a non-identifier name drops every label",
			format: "vec",
			params: []model.EventParam{data("amount", i128), data("not valid", u32)},
			want:   []string{"export type TransferEventData = readonly [sdk.i128, sdk.u32];\n"},
		},
		{
			name:   "vec with no data parameters is an empty tuple",
			format: "vec",
			params: []model.EventParam{topic("from", address)},
			want:   []string{"export type TransferEventData = readonly [];\n"},
		},
		{
			name:   "single-value is the value itself",
			format: "single-value",
			params: []model.EventParam{topic("from", address), data("amount", i128)},
			want: []string{
				"/** Data payload of the Transfer event: a single value. */\nexport type TransferEventData = sdk.i128;\n",
			},
		},
		{
			name:   "single-value with no data parameter is void",
			format: "single-value",
			params: []model.EventParam{topic("from", address)},
			want:   []string{"export type TransferEventData = void;\n"},
		},
		{
			name:   "an unknown format declares nothing about the payload",
			format: "unknown",
			params: []model.EventParam{data("amount", i128)},
			want:   []string{"export type TransferEventData = unknown;\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codegen.TypeScript(eventInterface([]model.Event{{
				Name: "Transfer", Prefix: []string{"transfer"}, Params: tt.params, DataFormat: tt.format,
			}}), src)
			require.NoError(t, err)
			for _, want := range tt.want {
				assert.Contains(t, got, want)
			}
			assert.Contains(t, got, "export interface TransferEvent {\n  topics: TransferEventTopics;\n  data: TransferEventData;\n}\n")
		})
	}
}

// TestEventTopicsAndPrefix proves topic parameters are split from data
// ones, in declaration order, and that the prefix is a readonly tuple
// constant a consumer can filter on.
func TestEventTopicsAndPrefix(t *testing.T) {
	t.Parallel()

	got, err := codegen.TypeScript(eventInterface([]model.Event{{
		Name:   "Transfer",
		Doc:    "Tokens moved between accounts.",
		Prefix: []string{"transfer", "v1"},
		Params: []model.EventParam{
			{Name: "from", Doc: "Sender.", Type: model.Scalar(model.KindAddress), Location: "topic-list"},
			data("amount", model.Scalar(model.KindI128)),
			topic("to", model.Scalar(model.KindAddress)),
		},
		DataFormat: "single-value",
	}}), src)
	require.NoError(t, err)

	assert.Contains(t, got, "export const TransferEventPrefix = [\"transfer\", \"v1\"] as const;\n")
	assert.Contains(t, got, "export interface TransferEventTopics {\n  /**\n   * Sender.\n   */\n  from: string;\n  to: string;\n}\n")
	assert.Contains(t, got, "/**\n * Tokens moved between accounts.\n */\nexport interface TransferEvent {\n")
	assert.NotContains(t, got, "amount: sdk.i128;\n  to", "a data parameter is not a topic")

	got, err = codegen.TypeScript(eventInterface([]model.Event{{
		Name: "Ping", Params: []model.EventParam{data("n", model.Scalar(model.KindU32))}, DataFormat: "map",
	}}), src)
	require.NoError(t, err)
	assert.Contains(t, got, "export const PingEventPrefix = [] as const;\n")
	assert.Contains(t, got, "export type PingEventTopics = Record<string, never>;\n")
	assert.Contains(t, got, "/**\n * The Ping event, as the SDK decodes it.\n */\nexport interface PingEvent {\n",
		"an undocumented event still says what it is")
}

// TestEventNamesDoNotCollideWithTypes proves an event never redeclares a
// type: an event and a struct sharing a name get distinct identifiers, and
// a struct already holding an event's derived name pushes every one of the
// event's names aside together.
func TestEventNamesDoNotCollideWithTypes(t *testing.T) {
	t.Parallel()

	transfer := model.Event{
		Name:       "Transfer",
		Prefix:     []string{"transfer"},
		Params:     []model.EventParam{data("amount", model.Scalar(model.KindI128))},
		DataFormat: "single-value",
	}
	field := []model.StructField{{Name: "amount", Type: model.Scalar(model.KindI128)}}

	t.Run("an event and a struct of the same name", func(t *testing.T) {
		t.Parallel()

		got, err := codegen.TypeScript(eventInterface([]model.Event{transfer},
			model.Struct{Name: "Transfer", Fields: field}), src)
		require.NoError(t, err)
		assert.Contains(t, got, "export interface Transfer {\n")
		assert.Contains(t, got, "export interface TransferEvent {\n")
		assertDeclaredOnce(t, got, "Transfer")
		assertDeclaredOnce(t, got, "TransferEvent")
	})

	t.Run("a struct holding the event's derived name", func(t *testing.T) {
		t.Parallel()

		got, err := codegen.TypeScript(eventInterface([]model.Event{transfer},
			model.Struct{Name: "Transfer", Fields: field},
			model.Struct{Name: "TransferEventData", Fields: field}), src)
		require.NoError(t, err)
		assertDeclaredOnce(t, got, "TransferEventData")
		assert.Contains(t, got, "export const Transfer_EventPrefix = [\"transfer\"] as const;\n")
		assert.Contains(t, got, "export type Transfer_EventTopics = Record<string, never>;\n")
		assert.Contains(t, got, "export type Transfer_EventData = sdk.i128;\n")
		assert.Contains(t, got, "export interface Transfer_Event {\n  topics: Transfer_EventTopics;\n  data: Transfer_EventData;\n}\n")
	})

	t.Run("an event whose name is not an identifier", func(t *testing.T) {
		t.Parallel()

		odd := transfer
		odd.Name = "my-event"
		got, err := codegen.TypeScript(eventInterface([]model.Event{odd}), src)
		require.NoError(t, err)
		assert.Contains(t, got, "export interface ContractEvent_6d792d6576656e74Event {\n")
		assert.Contains(t, got, "/** Prefix topics of the my-event event, to filter on. */\n")
	})
}

// TestEventSingleValueWithSeveralDataParamsFails proves an inconsistent
// spec is reported rather than generating a payload type that is wrong.
func TestEventSingleValueWithSeveralDataParamsFails(t *testing.T) {
	t.Parallel()

	_, err := codegen.TypeScript(eventInterface([]model.Event{{
		Name:       "Bad",
		Params:     []model.EventParam{data("a", model.Scalar(model.KindU32)), data("b", model.Scalar(model.KindU32))},
		DataFormat: "single-value",
	}}), src)
	require.ErrorContains(t, err, "event Bad has single-value data but 2 data parameters")
}

// assertDeclaredOnce fails unless exactly one top-level declaration binds
// name.
func assertDeclaredOnce(t *testing.T, source, name string) {
	t.Helper()

	n := 0
	for _, kw := range []string{"interface ", "type ", "const ", "enum "} {
		n += strings.Count(source, "export "+kw+name+" ")
	}
	assert.Equal(t, 1, n, "%s must be declared exactly once", name)
}

// TestEventNameCannotEndAComment proves an event name, which comes from an
// untrusted spec, cannot close the doc comments it is written into.
func TestEventNameCannotEndAComment(t *testing.T) {
	t.Parallel()

	got, err := codegen.TypeScript(eventInterface([]model.Event{{
		Name: "x*/ export const pwned = 1; /*", DataFormat: "map",
	}}), src)
	require.NoError(t, err)
	assert.NotContains(t, got, "x*/")
	assert.Contains(t, got, `/** Prefix topics of the x*\/ export const pwned = 1; /* event, to filter on. */`)
}
