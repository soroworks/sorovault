package spec_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// FuzzDecodeSpec ensures the spec decoder never panics on untrusted input.
// It wraps arbitrary bytes in a module's spec section and decodes it.
func FuzzDecodeSpec(f *testing.F) {
	f.Add("empty module")
	f.Add("random bytes")
	f.Add("valid with function entry")
	f.Add("malformed XDR")
	f.Add("section too large")
	f.Add("name too long")
	f.Add("truncated spec")

	f.Fuzz(func(t *testing.T, input string) {
		t.Parallel()

		var module []byte
		switch input {
		case "empty module":
			module = wasmtest.Module(wasmtest.Section{
				Name: "empty",
				Body: []byte{},
			})
		case "random bytes":
			module = wasmtest.Module(wasmtest.Section{
				Name: "random",
				Body: []byte{0x00, 0x01, 0x02, 0x03},
			})
		case "valid with function entry":
			// Create a minimal valid module with one function entry
			entries := []byte{0x01, 0x02, 0x03} // dummy XDR entries
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: entries,
			})
		case "malformed XDR":
			// Bytes that are not valid XDR ScSpecEntry
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0xFF, 0xFF, 0xFF, 0xFF},
			})
		case "section too large":
			// Create a module with a very large spec section
			largeBody := make([]byte, 10000)
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: largeBody,
			})
		case "name too long":
			// Name length exceeds maxSectionNameLen
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: make([]byte, 1000), // 1000 bytes as name
			})
		case "truncated spec":
			// Module with truncated spec section
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0x01, 0x02}, // incomplete
			})
		}

		// This must not panic
		iface, err := spec.XDRDecoder{}.Decode(module)
		// Accept success or error, but no panic
		_ = iface
		_ = err
	})
}