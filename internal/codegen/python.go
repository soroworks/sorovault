package codegen

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/soroworks/sorovault/internal/model"
)

// pyReserved are the module's own names: a contract type called Client
// would otherwise shadow the client it belongs to. Type names are claimed
// against these too, so nothing in the file collides with anything else.
var pyReserved = map[string]bool{
	"Client": true, "connect": true,
	"CONTRACT_ID": true, "NETWORK": true, "WASM_HASH": true, "ERROR_CODES": true,
	"_error": true, "_optional": true,
}

// pyLocals are the names a generated function body binds or references, so
// a parameter with one of these names would shadow what the encoding
// needs. Python's keywords are included because they cannot be used as
// names at all.
var pyLocals = map[string]bool{
	// Keywords.
	"False": true, "None": true, "True": true, "and": true, "as": true,
	"assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true, "global": true,
	"if": true, "import": true, "in": true, "is": true, "lambda": true,
	"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
	"return": true, "try": true, "while": true, "with": true, "yield": true,
	"match": true, "case": true, "type": true, "self": true,
	// What the bodies reference.
	"scval": true, "stellar_xdr": true, "_error": true, "_optional": true,
}

// Python renders a Python client for iface as one module that builds on
// stellar-sdk (the PyPI package that carries the Soroban support used
// here).
//
// The output declares every user-defined type — a struct becomes a
// dataclass whose to_scval follows SEP-48's map of field-name symbols, a
// union a tagged dataclass with a constructor per case, an enum an
// IntEnum encoded as the spec's u32 — and a Client class with one method
// per contract function. A method encodes its arguments and hands them to
// the SDK's ContractClient, which simulates the call and returns an
// AssembledTransaction ready to sign and submit; the call's return value
// arrives as an SCVal for the caller to decode with stellar_sdk.scval,
// because that is what the SDK hands back.
//
// Output is deterministic: types are declared in sorted order and
// functions in interface order, so the same interface always generates
// the same bytes.
func Python(iface *model.Interface, src Source) (string, error) {
	g := &pyGen{
		iface:      iface,
		errorEnums: map[string]bool{},
		names:      map[string]string{},
		claimed:    map[string]bool{},
		methods:    map[string]bool{},
		constructs: map[string]bool{},
		helpers:    map[string]bool{},
		imports:    map[string]bool{},
	}
	for _, e := range iface.Types.ErrorEnums {
		g.errorEnums[e.Name] = true
	}

	for name := range pyReserved {
		g.claimed[name] = true
	}

	// Every declared type reserves its Python name up front, so a
	// reference from anywhere in the file resolves to the same class.
	for _, s := range sortedBy(iface.Types.Structs, func(s model.Struct) string { return s.Name }) {
		g.claimType(s.Name)
	}
	for _, u := range sortedBy(iface.Types.Unions, func(u model.Union) string { return u.Name }) {
		g.claimType(u.Name)
	}
	for _, e := range sortedBy(iface.Types.Enums, func(e model.Enum) string { return e.Name }) {
		g.claimType(e.Name)
	}
	for _, e := range sortedBy(iface.Types.ErrorEnums, func(e model.ErrorEnum) string { return e.Name }) {
		g.claimType(e.Name)
	}

	// Walking every type up front reports one the client cannot carry
	// before any output exists.
	g.walkTypes()

	g.writeHead(src)
	g.writeConstants(src)
	g.writeErrorCodes()
	g.writeTypes()
	g.writeHelpers()
	g.writeClient()

	if g.err != nil {
		return "", g.err
	}
	return g.head.String() + g.body(), nil
}

type pyGen struct {
	head       strings.Builder // header comments and module docstring
	b          strings.Builder // body, starting with the imports
	iface      *model.Interface
	errorEnums map[string]bool
	names      map[string]string // declared type name -> class name
	claimed    map[string]bool   // every top-level name in use
	methods    map[string]bool   // method names on Client
	constructs map[string]bool   // union constructor function names
	helpers    map[string]bool   // helper functions the bodies reference
	imports    map[string]bool   // typing and module names the file uses
	err        error
}

func (g *pyGen) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// useImport records a name the import section must bring in.
func (g *pyGen) useImport(name string) {
	g.imports[name] = true
}

// helper records that a body references a helper, so it gets written.
func (g *pyGen) helper(name string) {
	g.helpers[name] = true
}

// claimType reserves the Python name for a contract type. Two types that
// differ only in case can map to the same identifier, so a name already
// taken gains underscores until it is unique.
func (g *pyGen) claimType(name string) {
	if _, ok := g.names[name]; ok {
		return
	}
	pyName := name
	if pyName == "" || !isPythonIdentStart([]rune(pyName)[0]) {
		pyName = "T" + pyName
	}
	for g.claimed[pyName] {
		pyName += "_"
	}
	g.claimed[pyName] = true
	g.names[name] = pyName
}

// className is the Python class of a declared type, claiming it on first
// use so a reference to a type the spec never declares still renders.
func (g *pyGen) className(name string) string {
	if _, ok := g.names[name]; !ok {
		g.claimType(name)
	}
	return g.names[name]
}

// walkTypes visits every type the file will carry, so an unsupported one
// is reported before any output is written.
func (g *pyGen) walkTypes() {
	for _, s := range g.iface.Types.Structs {
		for _, f := range s.Fields {
			_ = g.pyType(f.Type)
		}
	}
	for _, u := range g.iface.Types.Unions {
		for _, c := range u.Cases {
			for _, v := range c.Values {
				_ = g.pyType(v)
			}
		}
	}
	for _, fn := range g.iface.Functions {
		for _, in := range fn.Inputs {
			_ = g.pyType(in.Type)
		}
	}
}

func (g *pyGen) writeHead(src Source) {
	w := &g.head
	fmt.Fprintf(w, "# Code generated by SoroVault from contract %s on %s. DO NOT EDIT.\n", src.ContractID, src.Network)
	fmt.Fprintf(w, "# Interface version (WASM hash): %s\n", src.WasmHash)
	if g.iface.Meta != nil {
		if v := g.iface.Meta["rssdkver"]; v != "" {
			fmt.Fprintf(w, "# Built with soroban-sdk %s\n", oneLine(v))
		}
	}
	w.WriteString(`"""Typed Python client for the contract's functions.

Builds on stellar-sdk: every method encodes its arguments as SCVal and
hands them to ContractClient, which simulates the call and returns an
AssembledTransaction ready to sign and submit. The call's return value
arrives as an SCVal; decode it with stellar_sdk.scval.
"""
`)
}

func (g *pyGen) writeConstants(src Source) {
	fmt.Fprintf(&g.b, "\nCONTRACT_ID = %s\n", strconv.Quote(src.ContractID))
	fmt.Fprintf(&g.b, "NETWORK = %s\n", strconv.Quote(src.Network))
	fmt.Fprintf(&g.b, "WASM_HASH = %s\n", strconv.Quote(src.WasmHash))
}

// body assembles the module: the imports the bodies asked for, then the
// body itself. The imports come first because what they carry is only
// known once the bodies are written.
func (g *pyGen) body() string {
	var out strings.Builder
	out.WriteString(g.importBlock())
	out.WriteString(g.b.String())
	return out.String()
}

// importBlock renders the file's import block. The client class is in
// every file, so ContractClient, AssembledTransaction, SCVal and Any are
// unconditional; the rest arrived with the types and encodings.
func (g *pyGen) importBlock() string {
	g.useImport("Any")
	g.useImport("AssembledTransaction")
	g.useImport("ContractClient")
	g.useImport("stellar_xdr")

	var out strings.Builder
	out.WriteString("\nfrom __future__ import annotations\n\n")

	var stdlib, typing []string
	for name := range g.imports {
		switch name {
		case "dataclass", "IntEnum":
			stdlib = append(stdlib, name)
		case "Any", "Optional", "List", "Dict", "Tuple":
			typing = append(typing, name)
		}
	}
	sort.Strings(stdlib)
	sort.Strings(typing)
	switch {
	case len(stdlib) == 1 && stdlib[0] == "dataclass":
		out.WriteString("from dataclasses import dataclass\n")
	case len(stdlib) == 1 && stdlib[0] == "IntEnum":
		out.WriteString("from enum import IntEnum\n")
	case len(stdlib) == 2:
		out.WriteString("from dataclasses import dataclass\nfrom enum import IntEnum\n")
	}
	if len(typing) > 0 {
		fmt.Fprintf(&out, "from typing import %s\n", strings.Join(typing, ", "))
	}

	out.WriteString("\n")
	if g.imports["scval"] {
		out.WriteString("from stellar_sdk import scval, xdr as stellar_xdr\n")
	} else {
		out.WriteString("from stellar_sdk import xdr as stellar_xdr\n")
	}
	out.WriteString("from stellar_sdk.contract import AssembledTransaction, ContractClient\n")
	return out.String()
}

// writeTypes declares the contract's types, sorted by name so the output
// does not depend on the order the spec declared them in.
func (g *pyGen) writeTypes() {
	for _, s := range sortedBy(g.iface.Types.Structs, func(s model.Struct) string { return s.Name }) {
		g.writeStruct(s)
	}
	for _, u := range sortedBy(g.iface.Types.Unions, func(u model.Union) string { return u.Name }) {
		g.writeUnion(u)
	}
	for _, e := range sortedBy(g.iface.Types.Enums, func(e model.Enum) string { return e.Name }) {
		g.writeEnum(e)
	}
}

func (g *pyGen) writeStruct(s model.Struct) {
	name := g.className(s.Name)
	g.b.WriteString("\n\n")

	g.useImport("dataclass")
	fmt.Fprintf(&g.b, "@dataclass\nclass %s:\n", name)

	if s.IsTuple {
		// A tuple declared under its own name: positional fields,
		// encoded as the spec's vec. The spec names them "0", "1", ...,
		// which are not Python identifiers, so they get F-prefixed names.
		if strings.TrimSpace(s.Doc) == "" {
			g.b.WriteString("    \"\"\"A tuple the contract declared under this name.\"\"\"\n")
		} else {
			g.writeDoc(4, s.Doc)
		}
		types := make([]model.Type, 0, len(s.Fields))
		for i, f := range s.Fields {
			types = append(types, f.Type)
			fmt.Fprintf(&g.b, "    F%d: %s\n", i, g.pyType(f.Type))
		}
		g.b.WriteString("\n")
		g.writeTupleScVal(func(j int) string { return fmt.Sprintf("self.F%d", j) }, types)
		return
	}

	if strings.TrimSpace(s.Doc) == "" {
		g.b.WriteString("    \"\"\"A value the contract declares.\"\"\"\n")
	} else {
		g.writeDoc(4, s.Doc)
	}

	g.useImport("scval")
	g.useImport("stellar_xdr")
	used := map[string]bool{}
	specNames := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		field := pyFieldName(f.Name)
		for used[field] || pyLocals[field] {
			field += "_"
		}
		used[field] = true
		specNames[i] = field
		fmt.Fprintf(&g.b, "    %s: %s", field, g.pyType(f.Type))
		if strings.TrimSpace(f.Doc) != "" {
			fmt.Fprintf(&g.b, "  # %s", oneLine(f.Doc))
		}
		g.b.WriteString("\n")
	}

	g.b.WriteString("\n    def to_scval(self) -> stellar_xdr.SCVal:\n")
	g.b.WriteString("        \"\"\"Encode the value as the spec's map of field-name symbols.\"\"\"\n")
	fmt.Fprintf(&g.b, "        return scval.to_struct({\n")
	for i, f := range s.Fields {
		fmt.Fprintf(&g.b, "            %s: %s,\n", strconv.Quote(f.Name), g.encoder(f.Type, "self."+specNames[i]))
	}
	g.b.WriteString("        })\n")
}

// writeTupleScVal emits a to_scval that encodes positional values as the
// spec's vec. get renders the expression for position j.
func (g *pyGen) writeTupleScVal(get func(j int) string, types []model.Type) {
	g.useImport("scval")
	g.useImport("stellar_xdr")

	g.b.WriteString("    def to_scval(self) -> stellar_xdr.SCVal:\n")
	g.b.WriteString("        \"\"\"Encode the value as the spec's positional vec.\"\"\"\n")
	fmt.Fprintf(&g.b, "        return scval.to_tuple_struct([\n")
	for j, t := range types {
		fmt.Fprintf(&g.b, "            %s,\n", g.encoder(t, get(j)))
	}
	g.b.WriteString("        ])\n")
}

// writeUnion declares the union as a tagged dataclass — the case name
// plus the values it carries — and a constructor per case, which is how
// the SDK round-trips a union through the tagged vec the spec defines.
func (g *pyGen) writeUnion(u model.Union) {
	name := g.className(u.Name)
	g.b.WriteString("\n\n")

	g.useImport("dataclass")
	fmt.Fprintf(&g.b, "@dataclass\nclass %s:\n", name)
	if strings.TrimSpace(u.Doc) == "" {
		g.b.WriteString("    \"\"\"A tagged union the contract declares.\"\"\"\n")
	} else {
		g.writeDoc(4, u.Doc)
	}
	g.b.WriteString("    tag: str\n")
	g.useImport("List")
	g.useImport("stellar_xdr")
	g.b.WriteString("    values: List[stellar_xdr.SCVal]\n")

	g.b.WriteString("\n    def to_scval(self) -> stellar_xdr.SCVal:\n")
	g.b.WriteString("        \"\"\"Encode the value as the spec's tagged vec: the case name\n")
	g.b.WriteString("        as a symbol, followed by the case's values.\"\"\"\n")
	g.useImport("scval")
	g.b.WriteString("        return scval.to_vec([scval.to_symbol(self.tag), *self.values])\n")

	for _, c := range u.Cases {
		g.writeUnionCase(name, c)
	}
}

// writeUnionCase emits one constructor function. A case with no payload
// returns the tagged value directly; one with a payload encodes it while
// constructing, so the caller sees an encoding failure at the call.
func (g *pyGen) writeUnionCase(unionName string, c model.UnionCase) {
	name := pySnake(unionName) + "_" + pySnake(c.Name)
	for g.constructs[name] || g.claimed[name] {
		name += "_"
	}
	g.constructs[name] = true

	g.b.WriteString("\n")

	if len(c.Values) == 0 {
		fmt.Fprintf(&g.b, "def %s() -> %s:\n", name, unionName)
		if strings.TrimSpace(c.Doc) == "" {
			fmt.Fprintf(&g.b, "    \"\"\"The %s case, which carries nothing.\"\"\"\n", c.Name)
		} else {
			g.writeDoc(4, c.Doc)
		}
		fmt.Fprintf(&g.b, "    return %s(tag=%s, values=[])\n", unionName, strconv.Quote(c.Name))
		return
	}

	params := make([]string, len(c.Values))
	for i, v := range c.Values {
		params[i] = fmt.Sprintf("value%d: %s", i, g.pyType(v))
	}
	fmt.Fprintf(&g.b, "def %s(%s) -> %s:\n", name, strings.Join(params, ", "), unionName)
	if strings.TrimSpace(c.Doc) == "" {
		fmt.Fprintf(&g.b, "    \"\"\"The %s case, carrying its payload.\"\"\"\n", c.Name)
	} else {
		g.writeDoc(4, c.Doc)
	}
	g.useImport("scval")
	fmt.Fprintf(&g.b, "    return %s(tag=%s, values=[\n", unionName, strconv.Quote(c.Name))
	for i, v := range c.Values {
		fmt.Fprintf(&g.b, "        %s,\n", g.encoder(v, fmt.Sprintf("value%d", i)))
	}
	g.b.WriteString("    ])\n")
}

// writeEnum declares the enum as an IntEnum with a member per case: the
// spec says an enum value is a SCV_U32 of the case's number.
func (g *pyGen) writeEnum(e model.Enum) {
	name := g.className(e.Name)
	g.b.WriteString("\n\n")

	g.useImport("IntEnum")
	fmt.Fprintf(&g.b, "class %s(IntEnum):\n", name)
	if strings.TrimSpace(e.Doc) == "" {
		g.b.WriteString("    \"\"\"A C-style enumeration the contract declares.\"\"\"\n")
	} else {
		g.writeDoc(4, e.Doc)
	}
	used := map[string]bool{}
	for _, c := range e.Cases {
		member := pyMember(c.Name)
		for used[member] {
			member += "_"
		}
		used[member] = true
		g.writeComment(4, c.Doc)
		fmt.Fprintf(&g.b, "    %s = %d\n", member, c.Value)
	}
}

// writeErrorCodes emits one table of every contract error code, which is
// what the host reports. Codes from several error enums are merged; a
// code declared twice with a different name is an error, because the
// table could not say which meaning applies.
func (g *pyGen) writeErrorCodes() {
	type entry struct {
		code    uint32
		message string
	}
	byCode := map[uint32]entry{}
	for _, e := range g.iface.Types.ErrorEnums {
		for _, c := range e.Cases {
			if prev, dup := byCode[c.Value]; dup && prev.message != c.Name {
				g.fail(fmt.Errorf("codegen: error code %d is both %s and %s", c.Value, prev.message, c.Name))
				return
			}
			byCode[c.Value] = entry{c.Value, c.Name}
		}
	}
	if len(byCode) == 0 {
		return
	}

	codes := make([]uint32, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	sortUint32s(codes)

	g.b.WriteString("\n# ERROR_CODES maps every contract error code to its name, as the\n# host reports it.\nERROR_CODES: Dict[int, str] = {\n")
	g.useImport("Dict")
	for _, code := range codes {
		fmt.Fprintf(&g.b, "    %d: %s,\n", code, strconv.Quote(byCode[code].message))
	}
	g.b.WriteString("}\n")
}

// writeHelpers appends the helpers the bodies reached. A helper that is
// not used is not written: dead code in every file generated.
func (g *pyGen) writeHelpers() {
	if g.helpers["_error"] {
		g.b.WriteString(`
def _error(code: int) -> stellar_xdr.SCVal:
    """Encode a contract error code as SCV_ERROR: what the host reports
    for a contract error, and what SEP-48 says an error enum looks like.
    """
    return stellar_xdr.SCVal(
        stellar_xdr.SCValType.SCV_ERROR,
        error=stellar_xdr.SCError(
            stellar_xdr.ScErrorType.SCE_CONTRACT, contract_code=code
        ),
    )
`)
	}
	if g.helpers["_optional"] {
		g.b.WriteString(`
def _optional(value, encode):
    """Encode None as SCV_VOID and a value as itself: what SEP-48 says
    a contract expects for an Option.
    """
    if value is None:
        return scval.to_void()
    return encode(value)
`)
	}
}

// writeClient declares the Client class — one method per function in
// interface order — and the connect convenience function.
func (g *pyGen) writeClient() {
	g.b.WriteString(`

class Client:
    """Typed client for the contract's functions.

    Each method encodes its arguments and hands them to the SDK's
    ContractClient, which builds and simulates the invocation and returns
    an AssembledTransaction ready to sign and submit. Keyword arguments
    are forwarded to ContractClient.invoke, so source, signer, base_fee
    and the rest are all reachable from a call.
    """

    def __init__(
        self,
        rpc_url: str,
        network_passphrase: str,
        contract_id: str = CONTRACT_ID,
        **kwargs: Any,
    ) -> None:
        self._client = ContractClient(contract_id, rpc_url, network_passphrase, **kwargs)
`)

	for _, fn := range g.iface.Functions {
		g.writeMethod(fn)
	}

	g.b.WriteString(`

def connect(
    rpc_url: str,
    network_passphrase: str,
    contract_id: str = CONTRACT_ID,
    **kwargs: Any,
) -> Client:
    """Return a Client for the contract this file was generated from."""
    return Client(rpc_url, network_passphrase, contract_id, **kwargs)
`)
}

// writeMethod emits one method: the function's own words as the
// docstring, its parameters with the contract's types, and a body that
// encodes each argument and asks ContractClient to invoke.
func (g *pyGen) writeMethod(fn model.Function) {
	name := pySnake(fn.Name)
	if name == "" || !isPythonIdentStart([]rune(name)[0]) {
		name = "call"
	}
	for g.methods[name] || name == "__init__" {
		name += "_"
	}
	g.methods[name] = true

	params := g.paramNames(fn.Inputs)
	g.b.WriteString("\n")
	fmt.Fprintf(&g.b, "    def %s(self", name)
	for i, in := range fn.Inputs {
		fmt.Fprintf(&g.b, ", %s: %s", params[i], g.pyType(in.Type))
	}
	g.b.WriteString(", **kwargs: Any) -> AssembledTransaction[stellar_xdr.SCVal]:\n")

	// The docstring carries the signature as the spec declares it, the
	// function's own words, and each input's documentation.
	fmt.Fprintf(&g.b, "        \"\"\"Invoke the contract's %s function.\n", fn.Name)
	g.b.WriteString("\n")
	fmt.Fprintf(&g.b, "        %s\n", fn.Signature())
	if strings.TrimSpace(fn.Doc) != "" {
		g.b.WriteString("\n")
		for _, line := range strings.Split(strings.TrimSpace(fn.Doc), "\n") {
			fmt.Fprintf(&g.b, "        %s\n", oneLine(line))
		}
	}
	for i, in := range fn.Inputs {
		if strings.TrimSpace(in.Doc) != "" {
			fmt.Fprintf(&g.b, "        :param %s: %s\n", params[i], oneLine(in.Doc))
		}
	}
	g.b.WriteString("        \"\"\"\n")

	if len(fn.Inputs) == 0 {
		fmt.Fprintf(&g.b, "        return self._client.invoke(%s, **kwargs)\n", strconv.Quote(fn.Name))
		return
	}

	fmt.Fprintf(&g.b, "        return self._client.invoke(\n            %s,\n            parameters=[\n", strconv.Quote(fn.Name))
	for i, in := range fn.Inputs {
		fmt.Fprintf(&g.b, "                %s,\n", g.encoder(in.Type, params[i]))
	}
	g.b.WriteString("            ],\n            **kwargs,\n        )\n")
}

// paramNames converts the function's input names into Python parameters,
// falling back to a positional name for a parameter the contract left
// anonymous and suffixing names that would collide with the body's own
// bindings or a keyword.
func (g *pyGen) paramNames(inputs []model.Param) []string {
	seen := map[string]bool{}
	names := make([]string, len(inputs))
	for i, in := range inputs {
		name := pySnake(in.Name)
		if name == "" || !isPythonIdentStart([]rune(name)[0]) {
			name = fmt.Sprintf("arg%d", i)
		}
		for seen[name] || pyLocals[name] {
			name += "_"
		}
		seen[name] = true
		names[i] = name
	}
	return names
}

// pyType maps an interface type onto its Python annotation.
func (g *pyGen) pyType(t model.Type) string {
	switch t.Kind {
	case model.KindVal:
		g.useImport("stellar_xdr")
		return "stellar_xdr.SCVal"
	case model.KindBool:
		return "bool"
	case model.KindVoid:
		return "None"
	case model.KindError, model.KindU32, model.KindI32, model.KindU64, model.KindI64,
		model.KindU128, model.KindI128, model.KindU256, model.KindI256,
		model.KindTimepoint, model.KindDuration:
		return "int"
	case model.KindBytes, model.KindBytesN:
		return "bytes"
	case model.KindString, model.KindSymbol, model.KindAddress, model.KindMuxedAddress:
		return "str"
	case model.KindOption:
		g.useImport("Optional")
		return "Optional[" + g.child(t.Inner) + "]"
	case model.KindVec:
		g.useImport("List")
		return "List[" + g.child(t.Element) + "]"
	case model.KindMap:
		g.useImport("Dict")
		return "Dict[" + g.child(t.Key) + ", " + g.child(t.Value) + "]"
	case model.KindTuple:
		g.useImport("Tuple")
		parts := make([]string, 0, len(t.Elements))
		if len(t.Elements) == 0 {
			return "Tuple[()]"
		}
		for _, elem := range t.Elements {
			parts = append(parts, g.pyType(elem))
		}
		return "Tuple[" + strings.Join(parts, ", ") + "]"
	case model.KindResult:
		// Results only appear as return values, which the caller decodes
		// from the simulation; there is no value to carry in Python.
		g.fail(fmt.Errorf("codegen: cannot carry %s in a Python client value: a Result is only meaningful as a return value, which the caller reads from the simulation",
			model.Render(t)))
		return "None"
	case model.KindUDT:
		if g.errorEnums[t.Name] {
			// An error enum used as a value is its numeric code.
			return "int"
		}
		return g.className(t.Name)
	default:
		g.fail(fmt.Errorf("codegen: unsupported type kind %q", t.Kind))
		return "None"
	}
}

// child is pyType for a container's element, tolerating a missing one the
// same way the other generators do.
func (g *pyGen) child(t *model.Type) string {
	if t == nil {
		g.fail(fmt.Errorf("codegen: container type is missing its element type"))
		return "None"
	}
	return g.pyType(*t)
}

// encoder renders a call that encodes expr — a Python expression of type
// t — as the SCVal a contract expects for t. Containers use comprehensions
// and lambdas, so an encoding failure inside one propagates the same way
// a direct call would.
func (g *pyGen) encoder(t model.Type, expr string) string {
	switch t.Kind {
	case model.KindBool:
		g.useImport("scval")
		return "scval.to_bool(" + expr + ")"
	case model.KindU32:
		g.useImport("scval")
		return "scval.to_uint32(" + expr + ")"
	case model.KindI32:
		g.useImport("scval")
		return "scval.to_int32(" + expr + ")"
	case model.KindU64:
		g.useImport("scval")
		return "scval.to_uint64(" + expr + ")"
	case model.KindI64:
		g.useImport("scval")
		return "scval.to_int64(" + expr + ")"
	case model.KindTimepoint:
		g.useImport("scval")
		return "scval.to_timepoint(" + expr + ")"
	case model.KindDuration:
		g.useImport("scval")
		return "scval.to_duration(" + expr + ")"
	case model.KindU128:
		g.useImport("scval")
		return "scval.to_uint128(" + expr + ")"
	case model.KindI128:
		g.useImport("scval")
		return "scval.to_int128(" + expr + ")"
	case model.KindU256:
		g.useImport("scval")
		return "scval.to_uint256(" + expr + ")"
	case model.KindI256:
		g.useImport("scval")
		return "scval.to_int256(" + expr + ")"
	case model.KindBytes, model.KindBytesN:
		g.useImport("scval")
		return "scval.to_bytes(" + expr + ")"
	case model.KindString:
		g.useImport("scval")
		return "scval.to_string(" + expr + ")"
	case model.KindSymbol:
		g.useImport("scval")
		return "scval.to_symbol(" + expr + ")"
	case model.KindAddress, model.KindMuxedAddress:
		g.useImport("scval")
		return "scval.to_address(" + expr + ")"
	case model.KindError:
		g.helper("_error")
		g.useImport("stellar_xdr")
		return "_error(" + expr + ")"
	case model.KindVal:
		// Already an SCVal; the spec's Val accepts any variant.
		return expr
	case model.KindVoid:
		g.useImport("scval")
		return "scval.to_void()"
	case model.KindUDT:
		if g.errorEnums[t.Name] {
			g.helper("_error")
			g.useImport("stellar_xdr")
			return "_error(" + expr + ")"
		}
		return expr + ".to_scval()"
	case model.KindTuple:
		if t.Elements == nil {
			g.fail(fmt.Errorf("codegen: tuple type has no elements"))
			return "scval.to_tuple_struct([])"
		}
		g.useImport("scval")
		parts := make([]string, 0, len(t.Elements))
		for i, elem := range t.Elements {
			parts = append(parts, g.encoder(elem, fmt.Sprintf("%s[%d]", expr, i)))
		}
		return "scval.to_tuple_struct([" + strings.Join(parts, ", ") + "])"
	case model.KindOption:
		inner := t.Inner
		if inner == nil {
			g.fail(fmt.Errorf("codegen: option type is missing its body"))
			return "_optional(None, None)"
		}
		encoded := g.encoder(*inner, "wrapped")
		g.helper("_optional")
		g.useImport("scval")
		return fmt.Sprintf("_optional(%s, lambda wrapped: %s)", expr, encoded)
	case model.KindVec:
		elem := t.Element
		if elem == nil {
			g.fail(fmt.Errorf("codegen: vec type is missing its element type"))
			return "scval.to_vec([])"
		}
		g.useImport("scval")
		return fmt.Sprintf("scval.to_vec([%s for item in %s])", g.encoder(*elem, "item"), expr)
	case model.KindMap:
		if t.Key == nil || t.Value == nil {
			g.fail(fmt.Errorf("codegen: map type is missing its key or value type"))
			return "scval.to_map({})"
		}
		g.useImport("scval")
		return fmt.Sprintf("scval.to_map({%s: %s for key, val in %s.items()})",
			g.encoder(*t.Key, "key"), g.encoder(*t.Value, "val"), expr)
	default:
		g.fail(fmt.Errorf("codegen: unsupported type kind %q", t.Kind))
		return "scval.to_void()"
	}
}

// writeDoc writes a module-level docstring at the given indent.
func (g *pyGen) writeDoc(indent int, doc string) {
	pad := strings.Repeat(" ", indent)
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	g.b.WriteString(pad + `"""` + "\n")
	for _, line := range strings.Split(doc, "\n") {
		fmt.Fprintf(&g.b, "%s%s\n", pad, oneLine(line))
	}
	g.b.WriteString(pad + `"""` + "\n")
}

// writeComment writes an indented # comment block, or nothing when the
// doc is empty.
func (g *pyGen) writeComment(indent int, doc string) {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	pad := strings.Repeat(" ", indent)
	for _, line := range strings.Split(doc, "\n") {
		fmt.Fprintf(&g.b, "%s# %s\n", pad, oneLine(line))
	}
}

var pyWordSplit = regexp.MustCompile(`[^0-9A-Za-z]+`)

// pySnake converts an ABI name into a snake_case Python identifier: word
// boundaries and camel-case humps become underscores, keywords and the
// body's own names gain a trailing underscore.
func pySnake(name string) string {
	var b strings.Builder
	for _, part := range pyWordSplit.Split(name, -1) {
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('_')
		}
		runes := []rune(part)
		for i, r := range runes {
			if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// pyMember is the Python name of an enum case: upper case, so members
// read as the constants Python enums use.
func pyMember(name string) string {
	return strings.ToUpper(pySnake(name))
}

// pyFieldName is the Python name of a struct field.
func pyFieldName(name string) string {
	field := pySnake(name)
	if field == "" || !isPythonIdentStart([]rune(field)[0]) {
		field = "value" + field
	}
	return field
}

func isPythonIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}
