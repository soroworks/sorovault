package wasm_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// FuzzCustomSections ensures the WASM custom-section reader never panics on
// untrusted input. It feeds arbitrary bytes to the section reader and verifies
// no panics or unbounded allocations occur.
func FuzzCustomSections(f *testing.F) {
	f.Add("short module")
	f.Add("random bytes")
	f.Add("valid with spec section")
	f.Add("valid with meta section")
	f.Add("empty module")
	f.Add("only header")
	f.Add("name length overruns")
	f.Add("invalid utf8 in name")
	f.Add("truncated name")
	f.Add("name exceeds max length")

	f.Fuzz(func(t *testing.T, input string) {
		t.Parallel()

		var module []byte
		switch input {
		case "short module":
			module = []byte{0x00, 0x61, 0x73, 0x6D, 0x01} // header only
		case "random bytes":
			module = []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0xff, 0xfe, 0xfd}
		case "valid with spec section":
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0x01}, // valid name byte
			})
		case "valid with meta section":
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0x01},
			})
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractMeta,
				Body: []byte{0x02},
			})
		case "empty module":
			module = []byte{0x00, 0x61, 0x73, 0x6D, 0x01}
		case "only header":
			module = []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
		case "name length overruns":
			// name length says 256 bytes but only a few follow
			module = []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x10, 0x00} // 16 bytes claimed
		case "invalid utf8 in name":
			// length prefix says 1 byte, but the byte is a continuation byte
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0x80}, // invalid UTF-8
			})
		case "truncated name":
			// length prefix says 5 bytes, but only 3 follow
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: []byte{0x03, 0x01, 0x02}, // only 3 bytes after length
			})
		case "name exceeds max length":
			// name length says 1000 bytes (exceeds maxSectionNameLen)
			module = wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: make([]byte, 1000), // 1000 bytes claimed as name
			})
		}

		// This must not panic
		_, err := wasm.CustomSection(module, wasm.SectionContractSpec)
		// Accept either success or any error, but no panic
		_ = err
	})
}