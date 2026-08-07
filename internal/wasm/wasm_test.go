package wasm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

func TestCustomSection(t *testing.T) {
	t.Parallel()

	spec := []byte("spec-body")
	meta := []byte("meta-body")

	tests := []struct {
		name    string
		module  []byte
		section string
		want    []byte
	}{
		{
			name:    "only section",
			module:  wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: spec}),
			section: wasm.SectionContractSpec,
			want:    spec,
		},
		{
			name: "second of several",
			module: wasmtest.Module(
				wasmtest.Section{Name: wasm.SectionContractSpec, Body: spec},
				wasmtest.Section{Name: wasm.SectionContractMeta, Body: meta},
			),
			section: wasm.SectionContractMeta,
			want:    meta,
		},
		{
			name: "skips non-custom sections",
			module: wasmtest.ModuleWithNonCustom(
				1, []byte{0xDE, 0xAD, 0xBE, 0xEF},
				wasmtest.Section{Name: wasm.SectionContractSpec, Body: spec},
			),
			section: wasm.SectionContractSpec,
			want:    spec,
		},
		{
			name:    "empty body",
			module:  wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: nil}),
			section: wasm.SectionContractSpec,
			want:    []byte{},
		},
		{
			name: "name is not a prefix match",
			module: wasmtest.Module(
				wasmtest.Section{Name: "contractspecv0-extra", Body: []byte("wrong")},
				wasmtest.Section{Name: wasm.SectionContractSpec, Body: spec},
			),
			section: wasm.SectionContractSpec,
			want:    spec,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := wasm.CustomSection(tt.module, tt.section)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCustomSectionNotFound(t *testing.T) {
	t.Parallel()

	module := wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractMeta, Body: []byte("m")})

	_, err := wasm.CustomSection(module, wasm.SectionContractSpec)
	require.ErrorIs(t, err, wasm.ErrSectionNotFound)
	assert.Contains(t, err.Error(), wasm.SectionContractSpec)
}

func TestCustomSectionMalformed(t *testing.T) {
	t.Parallel()

	valid := wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: []byte("body")})

	tests := []struct {
		name    string
		module  []byte
		wantErr string
	}{
		{
			name:    "empty input",
			module:  nil,
			wantErr: "too short",
		},
		{
			name:    "header only, truncated",
			module:  []byte{0x00, 0x61, 0x73, 0x6D, 0x01},
			wantErr: "too short",
		},
		{
			name:    "bad magic",
			module:  []byte{'n', 'o', 'p', 'e', 0x01, 0x00, 0x00, 0x00},
			wantErr: "bad magic",
		},
		{
			name:    "unsupported version",
			module:  []byte{0x00, 0x61, 0x73, 0x6D, 0x63, 0x00, 0x00, 0x00},
			wantErr: "unsupported binary format version",
		},
		{
			name:    "section size overruns the module",
			module:  append(append([]byte{}, valid[:8]...), 0x00, 0x7F),
			wantErr: "truncated",
		},
		{
			name: "section size is not a valid uvarint",
			module: append(append([]byte{}, valid[:8]...),
				0x00, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80),
			wantErr: "overflows",
		},
		{
			name:    "name length overruns the payload",
			module:  append(append([]byte{}, valid[:8]...), 0x00, 0x02, 0x7F, 0x41),
			wantErr: "name truncated",
		},
		{
			// A module truncated mid-section must be an error, not a
			// silent "section not found".
			name:    "body cut short",
			module:  valid[:len(valid)-2],
			wantErr: "truncated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := wasm.CustomSection(tt.module, wasm.SectionContractSpec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotErrorIs(t, err, wasm.ErrSectionNotFound,
				"a malformed module must not be reported as a missing section")
		})
	}
}

// TestCustomSectionPaddedLength covers the format's allowance for
// non-minimal LEB128: a length may carry redundant continuation bytes, and
// the parser has to read it the same as the compact form.
func TestCustomSectionPaddedLength(t *testing.T) {
	t.Parallel()

	name := wasm.SectionContractSpec
	body := []byte("padded")

	payload := append(wasmtest.PaddedUvarint(uint32(len(name)), 3), name...)
	payload = append(payload, body...)

	module := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0x00}
	module = append(module, wasmtest.PaddedUvarint(uint32(len(payload)), 4)...)
	module = append(module, payload...)

	got, err := wasm.CustomSection(module, name)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestCustomSectionNames(t *testing.T) {
	t.Parallel()

	module := wasmtest.ModuleWithNonCustom(
		3, []byte{0x01, 0x02},
		wasmtest.Section{Name: wasm.SectionContractSpec, Body: []byte("s")},
		wasmtest.Section{Name: wasm.SectionContractEnvMeta, Body: []byte("e")},
		wasmtest.Section{Name: wasm.SectionContractMeta, Body: []byte("m")},
	)

	names, err := wasm.CustomSectionNames(module)
	require.NoError(t, err)
	assert.Equal(t, []string{
		wasm.SectionContractSpec,
		wasm.SectionContractEnvMeta,
		wasm.SectionContractMeta,
	}, names)
}

// TestCustomSectionAliases documents that the returned slice points into the
// caller's buffer, so callers who intend to retain it must copy.
func TestCustomSectionAliases(t *testing.T) {
	t.Parallel()

	module := wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: []byte("abc")})

	got, err := wasm.CustomSection(module, wasm.SectionContractSpec)
	require.NoError(t, err)
	require.Equal(t, []byte("abc"), got)

	got[0] = 'z'
	assert.Contains(t, string(module), "zbc")
}
