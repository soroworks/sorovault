// Package wasmtest builds small WebAssembly modules for tests.
//
// Decoding a contract interface starts from a WASM module, so tests across
// several packages need one to work with. Building a module here — rather
// than checking in a binary per case — keeps each test's input visible in
// the test itself, and makes it easy to construct the malformed modules the
// error paths need.
package wasmtest

import (
	"encoding/binary"
)

// Section is one custom section to place in a module.
type Section struct {
	Name string
	Body []byte
}

// Module builds a WebAssembly module containing only the given custom
// sections. That is not a module any runtime would accept — there is no code
// in it — but it is structurally valid to a section walker, which is all
// SoroVault reads.
func Module(sections ...Section) []byte {
	out := header()
	for _, s := range sections {
		out = append(out, customSection(s.Name, s.Body)...)
	}
	return out
}

// ModuleWithNonCustom builds a module that also contains a non-custom
// section, so tests can check that section walking skips over bodies it does
// not understand instead of misreading them.
func ModuleWithNonCustom(id byte, body []byte, sections ...Section) []byte {
	out := header()

	out = append(out, id)
	out = append(out, uvarint(uint32(len(body)))...)
	out = append(out, body...)

	for _, s := range sections {
		out = append(out, customSection(s.Name, s.Body)...)
	}
	return out
}

// header returns the 8-byte module preamble: the "\0asm" magic followed by
// the binary format version.
func header() []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6D}
	return binary.LittleEndian.AppendUint32(out, 1)
}

// customSection encodes one custom section: section ID 0, then the payload
// size, then a length-prefixed name followed by the body.
func customSection(name string, body []byte) []byte {
	payload := append(uvarint(uint32(len(name))), name...)
	payload = append(payload, body...)

	out := []byte{0x00}
	out = append(out, uvarint(uint32(len(payload)))...)
	return append(out, payload...)
}

// uvarint encodes a uint32 as unsigned LEB128.
func uvarint(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			return out
		}
	}
}

// PaddedUvarint encodes v as an unsigned LEB128 integer padded out to n
// bytes with redundant continuation bytes. The WASM binary format permits
// this, so the parser has to accept it.
func PaddedUvarint(v uint32, n int) []byte {
	out := make([]byte, 0, n)
	for i := range n {
		b := byte(v & 0x7F)
		v >>= 7
		if i < n-1 {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}
