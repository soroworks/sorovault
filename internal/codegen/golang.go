package codegen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/soroworks/sorovault/internal/model"
)

// goReservedIdentifiers are the names this file declares itself, plus the
// fields and functions that would collide with them. A user-defined type
// with one of these names gets a trailing underscore.
var goReservedIdentifiers = map[string]bool{
	"Client": true, "NewClient": true, "ContractID": true,
	"Network": true, "WasmHash": true, "ErrorCodes": true,
}

// goReservedLocals are Go's keywords plus the identifiers a generated
// method body uses itself; a parameter with one of these names gets a
// trailing underscore.
var goReservedLocals = map[string]bool{
	// Go keywords.
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
	// Names the generated bodies bind.
	"client": true, "args": true, "value": true, "err": true, "encode": true,
	"item": true, "key": true, "val": true, "wrapped": true,
}

// goImport paths the generated file can pull in. Which of them appear is
// decided by what the interface needs; fmt, strkey, txnbuild and xdr are
// in every file, because every file declares a client that invokes.
var (
	goImportFmt      = "fmt"
	goImportBig      = "math/big"
	goImportSort     = "sort"
	goImportBytes    = "bytes"
	goImportStrkey   = "github.com/stellar/go-stellar-sdk/strkey"
	goImportTxnbuild = "github.com/stellar/go-stellar-sdk/txnbuild"
	goImportXDR      = "github.com/stellar/go-stellar-sdk/xdr"
)

// Go renders a Go client for iface in a package named pkg (sanitized to a
// legal package name).
//
// The output builds on github.com/stellar/go-stellar-sdk — the module
// SoroVault itself depends on, so a generated client and the service
// share one dependency graph. Every contract function becomes a method on
// Client that encodes its arguments as xdr.ScVal and returns the
// txnbuild.InvokeHostFunction operation: the caller simulates it to read a
// return value, then signs and submits it. Decoding results is left to the
// SDK against the stored interface, because an operation is what a caller
// needs before fees, sequence numbers and signatures are known.
//
// Values follow SEP-48's mappings: a struct is a SCV_MAP of field-name
// symbols, a union is the SCV_VEC [case, values...], an enum is a u32, an
// Option is its value or SCV_VOID, and the wide integers Go has no
// primitive for are *big.Int — which cannot silently truncate the way a
// cast to uint64 would.
//
// Output is deterministic: types are declared in sorted order and
// functions in interface order, so the same interface always generates
// the same bytes.
func Go(iface *model.Interface, src Source, pkg string) (string, error) {
	g := &goGen{
		iface:      iface,
		pkg:        goPackageName(pkg),
		errorEnums: map[string]bool{},
		names:      map[string]string{},
		claimed:    map[string]bool{},
		methods:    map[string]bool{},
		tuples:     map[string]string{},
		helpers:    map[string]bool{},
		imports:    map[string]bool{},
	}
	for _, e := range iface.Types.ErrorEnums {
		g.errorEnums[e.Name] = true
	}

	// The file's own names are claimed first: a contract type called
	// Client would otherwise shadow the client it belongs to.
	for name := range goReservedIdentifiers {
		g.claimed[name] = true
	}

	// Every declared type reserves its Go name up front, so a reference
	// from a function parameter, another type, or anything written later
	// resolves to the same name.
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

	// Walk every type before writing anything: a tuple nested inside
	// another type gets its name here, and a type the Go client cannot
	// carry fails before half a file is built.
	g.walkTypes()

	g.writeHead(src)
	g.writeConstants(src)
	g.writeTypes()
	g.writeClient()
	g.writeHelpers()

	if g.err != nil {
		return "", g.err
	}

	// The import block sits between the package clause and the body, and
	// which imports there are is only known once the bodies are written,
	// so it is assembled here rather than as it goes.
	//
	// Formatting doubles as a syntax check: if the generator ever emits
	// something unparseable, the error surfaces here rather than in a
	// user's build.
	formatted, err := format.Source([]byte(g.head.String() + g.importBlock() + g.b.String()))
	if err != nil {
		return "", fmt.Errorf("codegen: generated Go does not parse: %w", err)
	}
	return string(formatted), nil
}

type goGen struct {
	head       strings.Builder // header comment and package clause
	b          strings.Builder // body, starting with the import block
	iface      *model.Interface
	pkg        string
	errorEnums map[string]bool
	names      map[string]string // declared type name -> Go name
	claimed    map[string]bool   // every top-level Go name in use
	methods    map[string]bool   // method names on Client, which share a namespace
	tuples     map[string]string // rendered tuple type -> generated name
	tupleDefs  []goTuple         // tuple types to declare, in discovery order
	helpers    map[string]bool   // helper functions the bodies reference
	imports    map[string]bool   // import paths the helpers and bodies need
	err        error
}

// goTuple is an anonymous tuple from the spec, given a name so it can be
// referenced: Go has no anonymous struct type that can be named.
type goTuple struct {
	name string
	typ  model.Type
}

func (g *goGen) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// useImport records that the file needs an import path.
func (g *goGen) useImport(path string) {
	g.imports[path] = true
}

// useHelper records that a body references a helper from goHelpers.
func (g *goGen) useHelper(name string) {
	g.helpers[name] = true
}

// call renders a helper invocation and records that the body references
// the helper. The arguments are the expressions inside the parens.
func (g *goGen) call(name string, args ...string) string {
	g.useHelper(name)
	return name + "(" + strings.Join(args, ", ") + ")"
}

// callRaw is call for a call whose argument list spans several
// expressions — closures among them — assembled by the caller.
func (g *goGen) callRaw(name string, rawArgs ...string) string {
	g.useHelper(name)
	return name + "(" + strings.Join(rawArgs, ", ") + ")"
}

// claimType reserves the Go name for a contract type. Two types that
// differ only in case or punctuation can map to the same identifier, so a
// name already taken gains underscores until it is unique.
func (g *goGen) claimType(name string) {
	if _, ok := g.names[name]; ok {
		return
	}
	goName := goExport(name)
	for g.claimed[goName] {
		goName += "_"
	}
	g.claimed[goName] = true
	g.names[name] = goName
}

// goTypeName is the Go name of a declared type, claiming it on first use
// so a reference to a type the spec never declares still renders.
func (g *goGen) goTypeName(name string) string {
	if _, ok := g.names[name]; !ok {
		g.claimType(name)
	}
	return g.names[name]
}

// methodName claims a name for a method on Client. Methods share one
// namespace, so two contract functions that convert to the same identifier
// gain underscores.
func (g *goGen) methodName(name string) string {
	out := goExport(name)
	for g.methods[out] {
		out += "_"
	}
	g.methods[out] = true
	return out
}

// walkTypes visits every type the file will carry, so nested tuples are
// named and unsupported types are reported before any output is written.
func (g *goGen) walkTypes() {
	for _, s := range g.iface.Types.Structs {
		for _, f := range s.Fields {
			_ = g.goType(f.Type)
		}
	}
	for _, u := range g.iface.Types.Unions {
		for _, c := range u.Cases {
			for _, v := range c.Values {
				_ = g.goType(v)
			}
		}
	}
	for _, fn := range g.iface.Functions {
		for _, in := range fn.Inputs {
			_ = g.goType(in.Type)
		}
	}
}

func (g *goGen) writeHead(src Source) {
	w := &g.head
	fmt.Fprintf(w, "// Code generated by SoroVault from contract %s on %s. DO NOT EDIT.\n", src.ContractID, src.Network)
	fmt.Fprintf(w, "// Interface version (WASM hash): %s\n", src.WasmHash)
	if g.iface.Meta != nil {
		if v := g.iface.Meta["rssdkver"]; v != "" {
			fmt.Fprintf(w, "// Built with soroban-sdk %s\n", oneLine(v))
		}
	}
	w.WriteString("//\n// Requires github.com/stellar/go-stellar-sdk.\n")
	fmt.Fprintf(w, "//\n// Package %s is a typed client for the contract's functions.\n", g.pkg)
	fmt.Fprintf(w, "\npackage %s\n", g.pkg)
}

func (g *goGen) writeConstants(src Source) {
	g.useImport(goImportFmt)
	g.b.WriteString(`
// ContractID is the contract every method invokes.
const ContractID = "` + src.ContractID + `"

// Network is the network the client was generated for.
const Network = "` + src.Network + `"

// WasmHash is the interface version the methods were generated from.
const WasmHash = "` + src.WasmHash + `"
`)
}

// importBlock renders the file's import block. The client type, its
// constructor and invoke are in every file, so strkey, txnbuild and xdr
// are unconditional; the rest arrived with the helpers and bodies.
func (g *goGen) importBlock() string {
	g.useImport(goImportFmt)
	g.useImport(goImportStrkey)
	g.useImport(goImportTxnbuild)
	g.useImport(goImportXDR)

	paths := make([]string, 0, len(g.imports))
	for path := range g.imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var b strings.Builder
	b.WriteString("\nimport (\n")
	for _, path := range paths {
		fmt.Fprintf(&b, "\t%q\n", path)
	}
	b.WriteString(")\n")
	return b.String()
}

// writeTypes declares the contract's types, sorted by name so the output
// does not depend on the order the spec declared them in.
func (g *goGen) writeTypes() {
	for _, s := range sortedBy(g.iface.Types.Structs, func(s model.Struct) string { return s.Name }) {
		g.writeStruct(s)
	}
	for _, u := range sortedBy(g.iface.Types.Unions, func(u model.Union) string { return u.Name }) {
		g.writeUnion(u)
	}
	for _, e := range sortedBy(g.iface.Types.Enums, func(e model.Enum) string { return e.Name }) {
		g.writeEnum(e)
	}
	for _, t := range g.tupleDefs {
		g.writeAnonymousTuple(t)
	}
	g.writeErrorCodes()
}

func (g *goGen) writeStruct(s model.Struct) {
	name := g.goTypeName(s.Name)
	g.b.WriteString("\n")
	g.writeDoc(s.Doc)

	if s.IsTuple {
		// A tuple declared under its own name: positional fields, encoded
		// as the spec's vec.
		fmt.Fprintf(&g.b, "type %s struct {\n", name)
		types := make([]model.Type, 0, len(s.Fields))
		for i, f := range s.Fields {
			types = append(types, f.Type)
			fmt.Fprintf(&g.b, "\tF%d %s\n", i, g.goType(f.Type))
		}
		g.b.WriteString("}\n")
		g.writeTupleScVal(name, types)
		return
	}

	fmt.Fprintf(&g.b, "type %s struct {\n", name)
	fields := make([]string, len(s.Fields))
	used := map[string]bool{}
	for i, f := range s.Fields {
		g.writeDoc(f.Doc)
		field := goFieldName(f.Name)
		for used[field] {
			field += "_"
		}
		used[field] = true
		fields[i] = field
		fmt.Fprintf(&g.b, "\t%s %s\n", field, g.goType(f.Type))
	}
	g.b.WriteString("}\n")
	g.writeStructScVal(name, s.Fields, fields)
}

// writeStructScVal emits the method that encodes the value as the spec's
// map of field-name symbols to field values. fields are the Go field
// names writeStruct chose, in the same order as the spec's fields.
func (g *goGen) writeStructScVal(name string, specFields []model.StructField, fields []string) {
	g.useHelper("scValSymbol")
	g.useHelper("scValEntries")

	fmt.Fprintf(&g.b, "\n// scVal encodes the value as the spec's map of fields.\nfunc (v %s) scVal() (xdr.ScVal, error) {\n", name)
	fmt.Fprintf(&g.b, "\tentries := make([]xdr.ScMapEntry, 0, %d)\n", len(specFields))
	g.b.WriteString("\tfor _, field := range []struct {\n\t\tname string\n\t\tvalue func() (xdr.ScVal, error)\n\t}{\n")
	for i, f := range specFields {
		expr := "v." + fields[i]
		fmt.Fprintf(&g.b, "\t\t{%q, func() (xdr.ScVal, error) { return %s }},\n", f.Name, g.encoder(f.Type, expr))
	}
	g.b.WriteString("\t} {\n")
	g.b.WriteString("\t\tkey, err := scValSymbol(field.name)\n")
	g.b.WriteString("\t\tif err != nil {\n\t\t\treturn xdr.ScVal{}, err\n\t\t}\n")
	g.b.WriteString("\t\tvalue, err := field.value()\n")
	g.b.WriteString("\t\tif err != nil {\n\t\t\treturn xdr.ScVal{}, err\n\t\t}\n")
	g.b.WriteString("\t\tentries = append(entries, xdr.ScMapEntry{Key: key, Val: value})\n")
	g.b.WriteString("\t}\n")
	g.b.WriteString("\treturn scValEntries(entries)\n}\n")
}

// writeUnion declares the union as a tagged struct — the case name plus
// the values it carries — and constructors that encode each case, which
// is how the SDK round-trips a union through the tagged vec the spec
// defines.
func (g *goGen) writeUnion(u model.Union) {
	name := g.goTypeName(u.Name)
	g.b.WriteString("\n")
	g.writeDoc(u.Doc)

	fmt.Fprintf(&g.b, "type %s struct {\n", name)
	g.b.WriteString("\t// Tag is the name of the case: Value, Transfer or whatever\n")
	g.b.WriteString("\t// the contract declared.\n")
	fmt.Fprintf(&g.b, "\tTag string\n")
	g.b.WriteString("\t// Values is the case's payload, in declaration order.\n")
	fmt.Fprintf(&g.b, "\tValues []xdr.ScVal\n")
	g.b.WriteString("}\n")

	for _, c := range u.Cases {
		g.writeUnionCase(name, c)
	}

	g.useHelper("scValSymbol")
	g.useHelper("scValVec")
	fmt.Fprintf(&g.b, "\n// scVal encodes the value as the spec's tagged vec: the case name\n// as a symbol, followed by the case's values.\nfunc (v %s) scVal() (xdr.ScVal, error) {\n", name)
	g.b.WriteString("\ttag, err := scValSymbol(v.Tag)\n")
	g.b.WriteString("\tif err != nil {\n\t\treturn xdr.ScVal{}, err\n\t}\n")
	g.b.WriteString("\treturn scValVec(append([]xdr.ScVal{tag}, v.Values...)...)\n}\n")
}

// writeUnionCase emits one constructor. A case with no payload is a plain
// value; one with a payload encodes it before returning, so the caller
// sees the encoding failure at construction.
func (g *goGen) writeUnionCase(unionName string, c model.UnionCase) {
	name := unionName + goExport(c.Name)
	for g.claimed[name] {
		name += "_"
	}
	g.claimed[name] = true

	g.b.WriteString("\n")
	g.writeDoc(c.Doc)

	if len(c.Values) == 0 {
		fmt.Fprintf(&g.b, "// %s is the %s case, which carries nothing.\n", name, c.Name)
		fmt.Fprintf(&g.b, "func %s() %s {\n", name, unionName)
		fmt.Fprintf(&g.b, "\treturn %s{Tag: %q}\n", unionName, c.Name)
		g.b.WriteString("}\n")
		return
	}

	params := make([]string, len(c.Values))
	for i, v := range c.Values {
		params[i] = fmt.Sprintf("value%d %s", i, g.goType(v))
	}
	fmt.Fprintf(&g.b, "// %s is the %s case, carrying its payload.\n", name, c.Name)
	fmt.Fprintf(&g.b, "func %s(%s) (%s, error) {\n", name, strings.Join(params, ", "), unionName)
	fmt.Fprintf(&g.b, "\tvalues := make([]xdr.ScVal, 0, %d)\n", len(c.Values))
	g.b.WriteString("\tfor _, encode := range []func() (xdr.ScVal, error){\n")
	for i, v := range c.Values {
		fmt.Fprintf(&g.b, "\t\tfunc() (xdr.ScVal, error) { return %s },\n", g.encoder(v, fmt.Sprintf("value%d", i)))
	}
	g.b.WriteString("\t} {\n")
	g.b.WriteString("\t\tvalue, err := encode()\n")
	g.b.WriteString("\t\tif err != nil {\n")
	fmt.Fprintf(&g.b, "\t\t\treturn %s{}, fmt.Errorf(%q, err)\n", unionName, c.Name+": %w")
	g.b.WriteString("\t\t}\n")
	g.b.WriteString("\t\tvalues = append(values, value)\n")
	g.b.WriteString("\t}\n")
	fmt.Fprintf(&g.b, "\treturn %s{Tag: %q, Values: values}, nil\n}\n", unionName, c.Name)
}

// writeEnum declares the enum as a uint32 with a constant per case: the
// spec says an enum value is a SCV_U32 of the case's number, and Go's
// named integer keeps the values from being passed around raw.
func (g *goGen) writeEnum(e model.Enum) {
	name := g.goTypeName(e.Name)
	g.b.WriteString("\n")
	g.writeDoc(e.Doc)

	fmt.Fprintf(&g.b, "type %s uint32\n", name)
	if len(e.Cases) == 0 {
		return
	}

	g.b.WriteString("\nconst (\n")
	for _, c := range e.Cases {
		g.writeDoc(c.Doc)
		constName := name + goExport(c.Name)
		for g.claimed[constName] {
			constName += "_"
		}
		g.claimed[constName] = true
		fmt.Fprintf(&g.b, "\t%s %s = %d\n", constName, name, c.Value)
	}
	g.b.WriteString(")\n")
}

func (g *goGen) writeAnonymousTuple(t goTuple) {
	g.b.WriteString("\n")
	g.writeDoc(fmt.Sprintf(
		"%s is the contract's inline tuple %s, given a name because Go has\nno anonymous type that can be referenced.",
		t.name, model.Render(t.typ)))
	fmt.Fprintf(&g.b, "type %s struct {\n", t.name)
	types := make([]model.Type, 0, len(t.typ.Elements))
	for i, elem := range t.typ.Elements {
		types = append(types, elem)
		fmt.Fprintf(&g.b, "\tF%d %s\n", i, g.goType(elem))
	}
	g.b.WriteString("}\n")
	g.writeTupleScVal(t.name, types)
}

// writeTupleScVal emits the method that encodes a tuple as the spec's
// positional vec.
func (g *goGen) writeTupleScVal(name string, types []model.Type) {
	g.useHelper("scValVec")

	fmt.Fprintf(&g.b, "\n// scVal encodes the value as the spec's positional vec.\nfunc (v %s) scVal() (xdr.ScVal, error) {\n", name)
	fmt.Fprintf(&g.b, "\tvalues := make([]xdr.ScVal, 0, %d)\n", len(types))
	g.b.WriteString("\tfor _, encode := range []func() (xdr.ScVal, error){\n")
	for i, t := range types {
		fmt.Fprintf(&g.b, "\t\tfunc() (xdr.ScVal, error) { return %s },\n", g.encoder(t, fmt.Sprintf("v.F%d", i)))
	}
	g.b.WriteString("\t} {\n")
	g.b.WriteString("\t\tvalue, err := encode()\n")
	g.b.WriteString("\t\tif err != nil {\n\t\t\treturn xdr.ScVal{}, err\n\t\t}\n")
	g.b.WriteString("\t\tvalues = append(values, value)\n")
	g.b.WriteString("\t}\n")
	g.b.WriteString("\treturn scValVec(values...)\n}\n")
}

// writeErrorCodes emits one table of every contract error code, which is
// what the host reports. Codes from several error enums are merged; a code
// declared twice with a different name is an error, because the table
// could not say which meaning applies.
func (g *goGen) writeErrorCodes() {
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

	g.b.WriteString("\n// ErrorCodes maps every contract error code to its name, as the\n// host reports it.\nvar ErrorCodes = map[uint32]string{\n")
	for _, code := range codes {
		fmt.Fprintf(&g.b, "\t%d: %q,\n", code, byCode[code].message)
	}
	g.b.WriteString("}\n")
}

// writeClient declares the Client type, its constructor, the invoke
// helper, and one method per function in interface order.
func (g *goGen) writeClient() {
	g.b.WriteString(`
// Client builds InvokeHostFunction operations for the contract's
// functions. Each method encodes its arguments and returns the operation:
// simulate it to read a return value, sign it to submit the call.
type Client struct {
	// ContractID is the contract every method invokes.
	ContractID string
	// Network is the network the contract was registered on when this
	// file was generated.
	Network string
	// WasmHash is the interface version the methods were generated from.
	WasmHash string
}

// NewClient returns a client for the contract this file was generated from.
func NewClient(contractID, network string) *Client {
	return &Client{ContractID: contractID, Network: network, WasmHash: WasmHash}
}

// invoke builds the InvokeHostFunction operation for fn: the contract
// address, the function name and the encoded arguments, plus the
// source-account authorization entry the host expects by default.
func (client *Client) invoke(fn string, args ...xdr.ScVal) (txnbuild.Operation, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, client.ContractID)
	if err != nil {
		return nil, fmt.Errorf("contract id: %w", err)
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)

	invocation := xdr.InvokeContractArgs{
		ContractAddress: xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		},
		FunctionName: xdr.ScSymbol(fn),
		Args:         args,
	}
	return &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type:          xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &invocation,
		},
		Auth: []xdr.SorobanAuthorizationEntry{{
			Credentials: xdr.SorobanCredentials{
				Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
			},
			RootInvocation: xdr.SorobanAuthorizedInvocation{
				Function: xdr.SorobanAuthorizedFunction{
					Type:       xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
					ContractFn: &invocation,
				},
			},
		}},
	}, nil
}
`)

	for _, fn := range g.iface.Functions {
		g.writeMethod(fn)
	}
}

// writeMethod emits one method: the function's own words, its signature
// as the spec declares it, and a body that encodes each argument and
// hands the lot to invoke.
func (g *goGen) writeMethod(fn model.Function) {
	name := g.methodName(fn.Name)
	params := g.paramNames(fn.Inputs)

	g.b.WriteString("\n")
	fmt.Fprintf(&g.b, "// %s invokes the contract's %s function:\n//\n", name, fn.Name)
	fmt.Fprintf(&g.b, "//\t%s\n", fn.Signature())
	if strings.TrimSpace(fn.Doc) != "" {
		g.b.WriteString("//\n")
	}
	g.writeDoc(fn.Doc)

	sig := make([]string, len(fn.Inputs))
	for i, in := range fn.Inputs {
		sig[i] = params[i] + " " + g.goType(in.Type)
	}
	fmt.Fprintf(&g.b, "func (client *Client) %s(%s) (txnbuild.Operation, error) {\n",
		name, strings.Join(sig, ", "))

	if len(fn.Inputs) == 0 {
		fmt.Fprintf(&g.b, "\treturn client.invoke(%q)\n}\n", fn.Name)
		return
	}

	fmt.Fprintf(&g.b, "\targs := make([]xdr.ScVal, 0, %d)\n", len(fn.Inputs))
	g.b.WriteString("\tfor _, encode := range []func() (xdr.ScVal, error){\n")
	for i, in := range fn.Inputs {
		fmt.Fprintf(&g.b, "\t\tfunc() (xdr.ScVal, error) { return %s },\n", g.encoder(in.Type, params[i]))
	}
	g.b.WriteString("\t} {\n")
	g.b.WriteString("\t\tvalue, err := encode()\n")
	g.b.WriteString("\t\tif err != nil {\n")
	fmt.Fprintf(&g.b, "\t\t\treturn nil, fmt.Errorf(%q, err)\n", fn.Name+": %w")
	g.b.WriteString("\t\t}\n")
	g.b.WriteString("\t\targs = append(args, value)\n")
	g.b.WriteString("\t}\n")
	fmt.Fprintf(&g.b, "\treturn client.invoke(%q, args...)\n}\n", fn.Name)
}

// paramNames converts the function's input names into Go parameters,
// falling back to a positional name for a parameter the contract left
// anonymous and suffixing names that would collide or clash with the
// method body's own bindings.
func (g *goGen) paramNames(inputs []model.Param) []string {
	seen := map[string]bool{}
	names := make([]string, len(inputs))
	for i, in := range inputs {
		name := goLocalName(in.Name, fmt.Sprintf("arg%d", i))
		for seen[name] {
			name += "_"
		}
		seen[name] = true
		names[i] = name
	}
	return names
}

// goType maps an interface type onto its Go form.
func (g *goGen) goType(t model.Type) string {
	switch t.Kind {
	case model.KindVal:
		return goImportXDR + ".ScVal"
	case model.KindBool:
		return "bool"
	case model.KindVoid:
		return "struct{}"
	case model.KindError:
		return "uint32"
	case model.KindU32:
		return "uint32"
	case model.KindI32:
		return "int32"
	case model.KindU64, model.KindTimepoint, model.KindDuration:
		return "uint64"
	case model.KindI64:
		return "int64"
	case model.KindU128, model.KindI128, model.KindU256, model.KindI256:
		g.useImport(goImportBig)
		return "*big.Int"
	case model.KindBytes:
		return "[]byte"
	case model.KindBytesN:
		return "[" + strconv.FormatUint(uint64(t.N), 10) + "]byte"
	case model.KindString, model.KindSymbol, model.KindAddress, model.KindMuxedAddress:
		return "string"
	case model.KindOption:
		return "*" + g.child(t.Inner)
	case model.KindVec:
		return "[]" + g.child(t.Element)
	case model.KindMap:
		return "map[" + g.child(t.Key) + "]" + g.child(t.Value)
	case model.KindResult:
		// Results only appear as return values, which the caller decodes
		// from the simulation; there is no value to carry in Go.
		g.fail(fmt.Errorf("codegen: cannot carry %s in a Go client value: a Result is only meaningful as a return value, which the caller reads from the simulation",
			model.Render(t)))
		return "struct{}"
	case model.KindTuple:
		return g.tupleName(t)
	case model.KindUDT:
		if g.errorEnums[t.Name] {
			// An error enum used as a value is its numeric code.
			return "uint32"
		}
		return g.goTypeName(t.Name)
	default:
		g.fail(fmt.Errorf("codegen: unsupported type kind %q", t.Kind))
		return "struct{}"
	}
}

// child is goType for a container's element, tolerating a missing one the
// same way the other generators do.
func (g *goGen) child(t *model.Type) string {
	if t == nil {
		g.fail(fmt.Errorf("codegen: container type is missing its element type"))
		return "struct{}"
	}
	return g.goType(*t)
}

// tupleName names an anonymous tuple from a hash of its rendering: the
// same tuple always gets the same name, and two different ones cannot
// collide.
func (g *goGen) tupleName(t model.Type) string {
	key := model.Render(t)
	if name, ok := g.tuples[key]; ok {
		return name
	}
	sum := sha256.Sum256([]byte(key))
	name := "Tuple_" + hex.EncodeToString(sum[:4])
	for g.claimed[name] {
		name += "_"
	}
	g.claimed[name] = true
	g.tuples[key] = name
	g.tupleDefs = append(g.tupleDefs, goTuple{name: name, typ: t})
	return name
}

// encoder renders a call that encodes expr — a Go expression of type t —
// as the ScVal a contract expects for t. Every encoding returns
// (xdr.ScVal, error), so containers pass element closures down and a
// failure anywhere propagates out of the method.
func (g *goGen) encoder(t model.Type, expr string) string {
	switch t.Kind {
	case model.KindBool:
		return g.call("scValBool", expr)
	case model.KindU32:
		return g.call("scValU32", expr)
	case model.KindI32:
		return g.call("scValI32", expr)
	case model.KindU64:
		return g.call("scValU64", expr)
	case model.KindI64:
		return g.call("scValI64", expr)
	case model.KindTimepoint:
		return g.call("scValTimepoint", expr)
	case model.KindDuration:
		return g.call("scValDuration", expr)
	case model.KindU128:
		return g.call("scValU128", expr)
	case model.KindI128:
		return g.call("scValI128", expr)
	case model.KindU256:
		return g.call("scValU256", expr)
	case model.KindI256:
		return g.call("scValI256", expr)
	case model.KindBytes:
		return g.call("scValBytes", expr)
	case model.KindBytesN:
		return g.call("scValBytes", expr+"[:]")
	case model.KindString:
		return g.call("scValString", expr)
	case model.KindSymbol:
		return g.call("scValSymbol", expr)
	case model.KindError:
		return g.call("scValError", expr)
	case model.KindAddress, model.KindMuxedAddress:
		return g.call("scValAddress", expr)
	case model.KindVal:
		return g.call("scValVal", expr)
	case model.KindVoid:
		return g.call("scValVoid")
	case model.KindUDT:
		if g.errorEnums[t.Name] {
			return g.call("scValError", expr)
		}
		return expr + ".scVal()"
	case model.KindTuple:
		return expr + ".scVal()"
	case model.KindVec:
		elem := t.Element
		if elem == nil {
			g.fail(fmt.Errorf("codegen: vec type is missing its element type"))
			return "scValVec()"
		}
		inner := g.encoder(*elem, "item")
		return g.callRaw("scValSlice", expr,
			fmt.Sprintf("func(item %s) (xdr.ScVal, error) { return %s }", g.goType(*elem), inner))
	case model.KindOption:
		inner := t.Inner
		if inner == nil {
			g.fail(fmt.Errorf("codegen: option type is missing its body"))
			return "scValOption(nil, nil)"
		}
		encoded := g.encoder(*inner, "wrapped")
		return g.callRaw("scValOption", expr,
			fmt.Sprintf("func(wrapped %s) (xdr.ScVal, error) { return %s }", g.goType(*inner), encoded))
	case model.KindMap:
		if t.Key == nil || t.Value == nil {
			g.fail(fmt.Errorf("codegen: map type is missing its key or value type"))
			return "scValMap(nil, nil, nil)"
		}
		encodeKey := g.encoder(*t.Key, "key")
		encodeValue := g.encoder(*t.Value, "val")
		return g.callRaw("scValMap", expr,
			fmt.Sprintf("func(key %s) (xdr.ScVal, error) { return %s }", g.goType(*t.Key), encodeKey),
			fmt.Sprintf("func(val %s) (xdr.ScVal, error) { return %s }", g.goType(*t.Value), encodeValue))
	default:
		g.fail(fmt.Errorf("codegen: unsupported type kind %q", t.Kind))
		return "scValVoid()"
	}
}

// writeDoc writes a doc string as // lines.
func (g *goGen) writeDoc(doc string) {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			g.b.WriteString("//\n")
			continue
		}
		fmt.Fprintf(&g.b, "// %s\n", line)
	}
}

// writeHelpers appends the subset of goHelpers the bodies reached,
// closing over what those helpers themselves call. A helper that is not
// used is not written: an unused function is dead weight in every file
// generated.
func (g *goGen) writeHelpers() {
	needed := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if needed[name] {
			return
		}
		needed[name] = true
		for _, helper := range goHelpers {
			if helper.name == name {
				for _, dep := range helper.deps {
					visit(dep)
				}
			}
		}
	}
	for name := range g.helpers {
		visit(name)
	}

	for _, helper := range goHelpers {
		if !needed[helper.name] {
			continue
		}
		for _, path := range helper.needs {
			g.useImport(path)
		}
		g.b.WriteString("\n" + helper.body)
	}
}

// goHelper is a function the generated file can reference. deps are the
// other helpers it calls; needs is an import path it requires beyond the
// ones every file has.
type goHelper struct {
	name  string
	deps  []string
	needs []string
	body  string
}

// goHelpers are emitted in this order when used. Each returns
// (xdr.ScVal, error) so a method's argument closures compose without
// special cases, even where the encoding cannot actually fail.
var goHelpers = []goHelper{
	{
		name: "scValBool",
		body: `// scValBool encodes a Go bool as SCV_BOOL.
func scValBool(v bool) (xdr.ScVal, error) {
	return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &v}, nil
}
`,
	},
	{
		name: "scValU32",
		body: `// scValU32 encodes v as SCV_U32.
func scValU32(v uint32) (xdr.ScVal, error) {
	u := xdr.Uint32(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil
}
`,
	},
	{
		name: "scValI32",
		body: `// scValI32 encodes v as SCV_I32.
func scValI32(v int32) (xdr.ScVal, error) {
	i := xdr.Int32(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &i}, nil
}
`,
	},
	{
		name: "scValU64",
		body: `// scValU64 encodes v as SCV_U64.
func scValU64(v uint64) (xdr.ScVal, error) {
	u := xdr.Uint64(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}, nil
}
`,
	},
	{
		name: "scValI64",
		body: `// scValI64 encodes v as SCV_I64.
func scValI64(v int64) (xdr.ScVal, error) {
	i := xdr.Int64(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &i}, nil
}
`,
	},
	{
		name: "scValTimepoint",
		body: `// scValTimepoint encodes seconds since the epoch as SCV_TIMEPOINT.
func scValTimepoint(v uint64) (xdr.ScVal, error) {
	t := xdr.TimePoint(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &t}, nil
}
`,
	},
	{
		name: "scValDuration",
		body: `// scValDuration encodes a number of seconds as SCV_DURATION.
func scValDuration(v uint64) (xdr.ScVal, error) {
	d := xdr.Duration(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil
}
`,
	},
	{
		name:  "scValU128",
		needs: []string{goImportBig},
		body: `// scValU128 encodes v as SCV_U128, refusing a value that does not fit
// rather than letting it wrap.
func scValU128(v *big.Int) (xdr.ScVal, error) {
	if v.Sign() < 0 || v.BitLen() > 128 {
		return xdr.ScVal{}, fmt.Errorf("u128: %s is outside [0, 2^128)", v)
	}
	parts := xdr.UInt128Parts{Hi: xdr.Uint64(limb(v, 64)), Lo: xdr.Uint64(limb(v, 0))}
	return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}, nil
}
`,
	},
	{
		name:  "scValI128",
		needs: []string{goImportBig},
		body: `// scValI128 encodes v as SCV_I128, refusing a value that does not fit.
func scValI128(v *big.Int) (xdr.ScVal, error) {
	min := new(big.Int).Lsh(big.NewInt(1), 127)
	min.Neg(min)
	if v.BitLen() > 128 || v.Cmp(min) < 0 {
		return xdr.ScVal{}, fmt.Errorf("i128: %s is outside [-2^127, 2^127)", v)
	}
	parts := xdr.Int128Parts{Hi: xdr.Int64(limb(v, 64)), Lo: xdr.Uint64(limb(v, 0))}
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &parts}, nil
}
`,
	},
	{
		name:  "scValU256",
		needs: []string{goImportBig},
		body: `// scValU256 encodes v as SCV_U256, refusing a value that does not fit.
func scValU256(v *big.Int) (xdr.ScVal, error) {
	if v.Sign() < 0 || v.BitLen() > 256 {
		return xdr.ScVal{}, fmt.Errorf("u256: %s is outside [0, 2^256)", v)
	}
	parts := xdr.UInt256Parts{
		HiHi: xdr.Uint64(limb(v, 192)),
		HiLo: xdr.Uint64(limb(v, 128)),
		LoHi: xdr.Uint64(limb(v, 64)),
		LoLo: xdr.Uint64(limb(v, 0)),
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &parts}, nil
}
`,
	},
	{
		name:  "scValI256",
		needs: []string{goImportBig},
		body: `// scValI256 encodes v as SCV_I256, refusing a value that does not fit.
func scValI256(v *big.Int) (xdr.ScVal, error) {
	min := new(big.Int).Lsh(big.NewInt(1), 255)
	min.Neg(min)
	if v.BitLen() > 256 || v.Cmp(min) < 0 {
		return xdr.ScVal{}, fmt.Errorf("i256: %s is outside [-2^255, 2^255)", v)
	}
	parts := xdr.Int256Parts{
		HiHi: xdr.Int64(limb(v, 192)),
		HiLo: xdr.Uint64(limb(v, 128)),
		LoHi: xdr.Uint64(limb(v, 64)),
		LoLo: xdr.Uint64(limb(v, 0)),
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &parts}, nil
}
`,
	},
	{
		name:  "limb",
		needs: []string{goImportBig},
		body: `// limb returns bits [shift, shift+64) of v as they sit in the two's
// complement Soroban's wide integers carry, so one helper splits any of
// them into parts.
func limb(v *big.Int, shift uint) uint64 {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	w := new(big.Int).Rsh(v, shift)
	w.And(w, mask)
	return w.Uint64()
}
`,
	},
	{
		name: "scValBytes",
		body: `// scValBytes encodes v as SCV_BYTES.
func scValBytes(v []byte) (xdr.ScVal, error) {
	b := xdr.ScBytes(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}, nil
}
`,
	},
	{
		name: "scValString",
		body: `// scValString encodes v as SCV_STRING.
func scValString(v string) (xdr.ScVal, error) {
	s := xdr.ScString(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &s}, nil
}
`,
	},
	{
		name: "scValSymbol",
		body: `// scValSymbol encodes v as SCV_SYMBOL.
func scValSymbol(v string) (xdr.ScVal, error) {
	s := xdr.ScSymbol(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &s}, nil
}
`,
	},
	{
		name: "scValError",
		body: `// scValError encodes a contract error code as SCV_ERROR: what the host
// reports for a contract error, and what SEP-48 says an error enum value
// looks like.
func scValError(code uint32) (xdr.ScVal, error) {
	c := xdr.Uint32(code)
	return xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &xdr.ScError{
		Type:         xdr.ScErrorTypeSceContract,
		ContractCode: &c,
	}}, nil
}
`,
	},
	{
		name:  "scValAddress",
		needs: []string{goImportStrkey},
		body: `// scValAddress parses a strkey address — account, contract, muxed,
// claimable balance or liquidity pool — into the SCV_ADDRESS a contract
// expects.
func scValAddress(s string) (xdr.ScVal, error) {
	version, payload, err := strkey.DecodeAny(s)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("address: %w", err)
	}
	switch version {
	case strkey.VersionByteAccountID:
		key := xdr.Uint256(payload)
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &xdr.AccountId{
				Type:    xdr.PublicKeyTypePublicKeyTypeEd25519,
				Ed25519: &key,
			},
		}}, nil
	case strkey.VersionByteContract:
		id := xdr.ContractId(payload)
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &id,
		}}, nil
	case strkey.VersionByteMuxedAccount:
		muxed, err := strkey.DecodeMuxedAccount(s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("muxed address: %w", err)
		}
		key := xdr.Uint256(muxed.Ed25519())
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeMuxedAccount,
			MuxedAccount: &xdr.MuxedEd25519Account{
				Id:      xdr.Uint64(muxed.ID()),
				Ed25519: key,
			},
		}}, nil
	case strkey.VersionByteClaimableBalance:
		if len(payload) != 33 || payload[0] != 0 {
			return xdr.ScVal{}, fmt.Errorf("address: unsupported claimable balance id")
		}
		id := xdr.Hash(payload[1:])
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeClaimableBalance,
			ClaimableBalanceId: &xdr.ClaimableBalanceId{
				Type: xdr.ClaimableBalanceIdTypeClaimableBalanceIdTypeV0,
				V0:   &id,
			},
		}}, nil
	case strkey.VersionByteLiquidityPool:
		id := xdr.PoolId(payload)
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type:           xdr.ScAddressTypeScAddressTypeLiquidityPool,
			LiquidityPoolId: &id,
		}}, nil
	default:
		return xdr.ScVal{}, fmt.Errorf("address: unsupported strkey version %d", version)
	}
}
`,
	},
	{
		name: "scValVal",
		body: `// scValVal passes a value the caller already holds as an ScVal through:
// the spec's Val accepts any variant.
func scValVal(v xdr.ScVal) (xdr.ScVal, error) {
	return v, nil
}
`,
	},
	{
		name: "scValVoid",
		body: `// scValVoid encodes the absence of a value as SCV_VOID.
func scValVoid() (xdr.ScVal, error) {
	return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
}
`,
	},
	{
		name: "scValVec",
		body: `// scValVec wraps values as SCV_VEC: a Vec's elements, a tuple's
// position, or a union's case name followed by its values.
func scValVec(values ...xdr.ScVal) (xdr.ScVal, error) {
	vec := xdr.ScVec(values)
	ptr := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &ptr}, nil
}
`,
	},
	{
		name:  "scValEntries",
		needs: []string{goImportSort, goImportBytes},
		body: `// scValEntries sorts entries the way the host expects a map —
// ascending by the XDR of the keys — and wraps them as SCV_MAP.
func scValEntries(entries []xdr.ScMapEntry) (xdr.ScVal, error) {
	sort.Slice(entries, func(i, j int) bool {
		a, _ := entries[i].Key.MarshalBinary()
		b, _ := entries[j].Key.MarshalBinary()
		return bytes.Compare(a, b) < 0
	})
	m := xdr.ScMap(entries)
	ptr := &m
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &ptr}, nil
}
`,
	},
	{
		name: "scValSlice",
		deps: []string{"scValVec"},
		body: `// scValSlice encodes a Vec: every element, then the lot as SCV_VEC.
func scValSlice[T any](values []T, encode func(T) (xdr.ScVal, error)) (xdr.ScVal, error) {
	items := make([]xdr.ScVal, 0, len(values))
	for _, value := range values {
		item, err := encode(value)
		if err != nil {
			return xdr.ScVal{}, err
		}
		items = append(items, item)
	}
	return scValVec(items...)
}
`,
	},
	{
		name: "scValOption",
		body: `// scValOption encodes None as SCV_VOID and Some as the wrapped value:
// what SEP-48 says a contract expects for an Option.
func scValOption[T any](value *T, encode func(T) (xdr.ScVal, error)) (xdr.ScVal, error) {
	if value == nil {
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
	}
	return encode(*value)
}
`,
	},
	{
		name:  "scValMap",
		deps:  []string{"scValEntries"},
		needs: []string{goImportFmt},
		body: `// scValMap encodes a Go map as SCV_MAP, failing when a key or a value
// cannot be encoded.
func scValMap[K, V any](values map[K]V, encodeKey func(K) (xdr.ScVal, error), encodeValue func(V) (xdr.ScVal, error)) (xdr.ScVal, error) {
	entries := make([]xdr.ScMapEntry, 0, len(values))
	for key, value := range values {
		k, err := encodeKey(key)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("map key: %w", err)
		}
		v, err := encodeValue(value)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("map value: %w", err)
		}
		entries = append(entries, xdr.ScMapEntry{Key: k, Val: v})
	}
	return scValEntries(entries)
}
`,
	},
}

var goIdentSplit = regexp.MustCompile(`[^0-9A-Za-z]+`)

// goExport converts an ABI name into an exported Go identifier: runs of
// anything that is not a letter or digit separate words, and each word
// starts upper case. A name that carries no letters at all still yields
// something compilable.
func goExport(name string) string {
	var b strings.Builder
	for _, part := range goIdentSplit.Split(name, -1) {
		if part == "" {
			continue
		}
		runes := []rune(part)
		b.WriteRune(unicode.ToUpper(runes[0]))
		b.WriteString(string(runes[1:]))
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		return "X" + out
	}
	return out
}

// goUnexport lower-cases the first rune of an exported identifier.
func goUnexport(name string) string {
	runes := []rune(name)
	return string(unicode.ToLower(runes[0])) + string(runes[1:])
}

// goFieldName is the Go name of a struct field. Fields are exported so a
// caller can read a value it decoded; the first rune is upper case, so a
// field name can never be a keyword.
func goFieldName(name string) string {
	return goExport(name)
}

// goLocalName is the Go name for a parameter: lower case, because a
// parameter is not exported, and suffixed when the keyword or the method
// body's own bindings would collide.
func goLocalName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	out := goUnexport(goExport(name))
	if goReservedLocals[out] {
		out += "_"
	}
	return out
}

// goPackageName turns anything into a legal Go package name, defaulting
// to contract when the name carries nothing usable.
func goPackageName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(b.String(), "0123456789_")
	if out == "" {
		return "contract"
	}
	return out
}

// sortUint32s sorts codes ascending for the ErrorCodes table.
func sortUint32s(items []uint32) {
	sort.Slice(items, func(i, j int) bool { return items[i] < items[j] })
}
