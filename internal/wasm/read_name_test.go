package wasm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/wasm"
)

func TestReadNameValid(t *testing.T) {
	t.Parallel()

	name := "testname"
	payload := []byte{0x0a, 0x74, 0x65, 0x73, 0x74, 0x6e, 0x61, 0x6d, 0x65}

	gotName, rest, err := wasm.ReadName(payload)
	require.NoError(t, err)
	assert.Equal(t, name, gotName)
	assert.Equal(t[len(name):], rest)
}

func TestReadNameLengthExceedsPayload(t *testing.T) {
	t.Parallel()

	// name length says 10 bytes, but only 5 bytes follow
	payload := []byte{0x0a, 0x74, 0x65, 0x73, 0x74}

	_, _, err := wasm.ReadName(payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}

func TestReadNameZeroLength(t *testing.T) {
	t.Parallel()

	// zero-length name
	payload := []byte{0x00}

	gotName, rest, err := wasm.ReadName(payload)
	require.NoError(t, err)
	assert.Equal(t, "", gotName)
	// rest should be empty since there's no payload after the name length
	assert.Equal(t, []byte{}, rest)
}

func TestReadNameInvalidUTF8(t *testing.T) {
	t.Parallel()

	// invalid UTF-8 bytes after the length prefix
	// 0x80 is a continuation byte without a leading byte
	payload := []byte{0x01, 0x80}

	_, _, err := wasm.ReadName(payload)
	assert.Error(t, err)
	// Should not panic, should return an error
	assert.Contains(t, err.Error(), "invalid")
}

func TestReadNameNameLongerThanRemaining(t *testing.T) {
	t.Parallel()

	// name length says 100 bytes, but only 5 bytes follow
	payload := []byte{0x64, 0x01, 0x02, 0x03, 0x04, 0x05}

	_, _, err := wasm.ReadName(payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}

func TestReadNameExactBoundaries(t *testing.T) {
	t.Parallel()

	// name length matches exactly the remaining bytes
	name := "hi"
	payload := []byte{0x02, 0x68, 0x65}

	gotName, rest, err := wasm.ReadName(payload)
	require.NoError(t, err)
	assert.Equal(t, name, gotName)
	// rest should be empty since we consumed exactly the payload
	assert.Equal(t, []byte{}, rest)
}