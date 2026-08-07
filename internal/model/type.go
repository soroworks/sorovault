package model

import (
	"strconv"
	"strings"
)

// Kind identifies which variant a Type is. The scalar kinds map one-to-one
// onto Soroban's primitive spec types; the remaining kinds are containers
// that carry nested Types, or a reference to a user-defined type.
type Kind string

// Scalar kinds.
const (
	KindVal          Kind = "val"
	KindBool         Kind = "bool"
	KindVoid         Kind = "void"
	KindError        Kind = "error"
	KindU32          Kind = "u32"
	KindI32          Kind = "i32"
	KindU64          Kind = "u64"
	KindI64          Kind = "i64"
	KindTimepoint    Kind = "timepoint"
	KindDuration     Kind = "duration"
	KindU128         Kind = "u128"
	KindI128         Kind = "i128"
	KindU256         Kind = "u256"
	KindI256         Kind = "i256"
	KindBytes        Kind = "bytes"
	KindString       Kind = "string"
	KindSymbol       Kind = "symbol"
	KindAddress      Kind = "address"
	KindMuxedAddress Kind = "muxed_address"
)

// Composite kinds.
const (
	KindOption Kind = "option"
	KindResult Kind = "result"
	KindVec    Kind = "vec"
	KindMap    Kind = "map"
	KindTuple  Kind = "tuple"
	KindBytesN Kind = "bytes_n"
	KindUDT    Kind = "udt"
)

// Type is a value type in a contract's interface.
//
// Exactly one group of fields is populated, selected by Kind: scalars
// populate none, KindVec sets Element, KindMap sets Key and Value, and so
// on. Display always carries a human-readable rendering of the whole type
// ("Option<Vec<u32>>"), so UIs and documentation generators do not each have
// to re-implement the traversal.
type Type struct {
	Kind    Kind   `json:"kind"`
	Display string `json:"display"`

	// Element is the item type of a KindVec.
	Element *Type `json:"element,omitempty"`
	// Key and Value are the halves of a KindMap.
	Key   *Type `json:"key,omitempty"`
	Value *Type `json:"value,omitempty"`
	// Ok and Error are the arms of a KindResult.
	Ok    *Type `json:"ok,omitempty"`
	Error *Type `json:"error,omitempty"`
	// Elements are the members of a KindTuple.
	Elements []Type `json:"elements,omitempty"`
	// Inner is the wrapped type of a KindOption.
	Inner *Type `json:"inner,omitempty"`
	// N is the fixed length of a KindBytesN.
	N uint32 `json:"n,omitempty"`
	// Name is the referenced type's name for a KindUDT.
	Name string `json:"name,omitempty"`
}

// scalarDisplay maps scalar kinds to how they are written in Rust source,
// which is the notation contract authors and consumers already read.
var scalarDisplay = map[Kind]string{
	KindVal:          "Val",
	KindBool:         "bool",
	KindVoid:         "()",
	KindError:        "Error",
	KindU32:          "u32",
	KindI32:          "i32",
	KindU64:          "u64",
	KindI64:          "i64",
	KindTimepoint:    "Timepoint",
	KindDuration:     "Duration",
	KindU128:         "u128",
	KindI128:         "i128",
	KindU256:         "u256",
	KindI256:         "i256",
	KindBytes:        "Bytes",
	KindString:       "String",
	KindSymbol:       "Symbol",
	KindAddress:      "Address",
	KindMuxedAddress: "MuxedAddress",
}

// Scalar builds a scalar Type of the given kind, with its Display filled in.
// It panics if kind is not a scalar, which can only happen through a
// programming error in this package.
func Scalar(kind Kind) Type {
	d, ok := scalarDisplay[kind]
	if !ok {
		panic("model.Scalar: not a scalar kind: " + string(kind))
	}
	return Type{Kind: kind, Display: d}
}

// Render returns the human-readable rendering of t, recomputing it from the
// type's structure rather than trusting the Display field. Decoders use it to
// populate Display; consumers that build Types by hand can use it too.
func Render(t Type) string {
	switch t.Kind {
	case KindOption:
		return "Option<" + renderChild(t.Inner) + ">"
	case KindVec:
		return "Vec<" + renderChild(t.Element) + ">"
	case KindMap:
		return "Map<" + renderChild(t.Key) + ", " + renderChild(t.Value) + ">"
	case KindResult:
		return "Result<" + renderChild(t.Ok) + ", " + renderChild(t.Error) + ">"
	case KindBytesN:
		return "BytesN<" + strconv.FormatUint(uint64(t.N), 10) + ">"
	case KindUDT:
		return t.Name
	case KindTuple:
		parts := make([]string, len(t.Elements))
		for i := range t.Elements {
			parts[i] = Render(t.Elements[i])
		}
		return "(" + strings.Join(parts, ", ") + ")"
	default:
		if d, ok := scalarDisplay[t.Kind]; ok {
			return d
		}
		return string(t.Kind)
	}
}

// renderChild renders a nested type, tolerating a nil pointer so that a
// partially-populated Type renders as something readable instead of panicking.
func renderChild(t *Type) string {
	if t == nil {
		return "?"
	}
	return Render(*t)
}

// Signature renders a function as a Rust-like one-line signature, for use in
// the browse UI and CLI output.
func (f Function) Signature() string {
	var b strings.Builder
	b.WriteString("fn ")
	b.WriteString(f.Name)
	b.WriteByte('(')
	for i, in := range f.Inputs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(in.Name)
		b.WriteString(": ")
		b.WriteString(Render(in.Type))
	}
	b.WriteByte(')')
	if len(f.Outputs) > 0 {
		b.WriteString(" -> ")
		b.WriteString(Render(f.Outputs[0]))
	}
	return b.String()
}
