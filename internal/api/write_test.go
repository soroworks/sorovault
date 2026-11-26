package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseBufferWrite(t *testing.T) {
	t.Parallel()

	buf := newResponseBuffer()
	payload := []byte("happy path response")

	n, err := buf.Write(payload)
	require.NoError(t, err, "writing to responseBuffer must not fail")
	assert.Equal(t, len(payload), n, "Write must return the count of bytes written")

	w := httptest.NewRecorder()
	buf.writeTo(w)

	assert.Equal(t, http.StatusOK, w.Code, "Write without prior WriteHeader must default status to 200 OK")
	assert.Equal(t, payload, w.Body.Bytes(), "writeTo must write the buffered payload")
}

// TestResponseBufferWriteEdgeCases covers table-driven edge cases for Write:
// empty input (nil and empty slice), boundary values (single byte, binary null,
// large payload), and status code preservation after explicit WriteHeader.
func TestResponseBufferWriteEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initialStatus int
		input         []byte
		wantN         int
		wantStatus    int
		wantBody      []byte
	}{
		{
			name:          "empty input nil slice",
			initialStatus: 0,
			input:         nil,
			wantN:         0,
			wantStatus:    http.StatusOK,
			wantBody:      nil,
		},
		{
			name:          "empty input zero-length slice",
			initialStatus: 0,
			input:         []byte{},
			wantN:         0,
			wantStatus:    http.StatusOK,
			wantBody:      nil,
		},
		{
			name:          "boundary value binary null byte",
			initialStatus: 0,
			input:         []byte{0x00},
			wantN:         1,
			wantStatus:    http.StatusOK,
			wantBody:      []byte{0x00},
		},
		{
			name:          "boundary value multibyte unicode characters",
			initialStatus: 0,
			input:         []byte("🌟 sorovault 🚀"),
			wantN:         len([]byte("🌟 sorovault 🚀")),
			wantStatus:    http.StatusOK,
			wantBody:      []byte("🌟 sorovault 🚀"),
		},
		{
			name:          "boundary value large payload",
			initialStatus: 0,
			input:         bytes.Repeat([]byte("a"), 64*1024),
			wantN:         64 * 1024,
			wantStatus:    http.StatusOK,
			wantBody:      bytes.Repeat([]byte("a"), 64*1024),
		},
		{
			name:          "boundary value explicit status preserved",
			initialStatus: http.StatusNotFound,
			input:         []byte("not found"),
			wantN:         9,
			wantStatus:    http.StatusNotFound,
			wantBody:      []byte("not found"),
		},
		{
			name:          "boundary value error status with empty body",
			initialStatus: http.StatusInternalServerError,
			input:         nil,
			wantN:         0,
			wantStatus:    http.StatusInternalServerError,
			wantBody:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			buf := newResponseBuffer()
			if tt.initialStatus != 0 {
				buf.WriteHeader(tt.initialStatus)
			}

			n, err := buf.Write(tt.input)
			require.NoError(t, err, "Write must not return an error")
			assert.Equal(t, tt.wantN, n, "Write must return the expected byte count")

			w := httptest.NewRecorder()
			buf.writeTo(w)

			assert.Equal(t, tt.wantStatus, w.Code, "responseBuffer must record or preserve expected status code")
			if len(tt.wantBody) == 0 {
				assert.Empty(t, w.Body.Bytes(), "empty inputs must result in an empty response body")
			} else {
				assert.Equal(t, tt.wantBody, w.Body.Bytes(), "buffered response body must match expected payload")
			}
		})
	}
}

func TestResponseBufferWriteSequential(t *testing.T) {
	t.Parallel()

	buf := newResponseBuffer()
	chunks := [][]byte{
		[]byte("chunk1-"),
		nil,
		[]byte(""),
		[]byte("chunk2"),
	}

	totalLen := 0
	for _, chunk := range chunks {
		n, err := buf.Write(chunk)
		require.NoError(t, err, "sequential write must succeed")
		assert.Equal(t, len(chunk), n, "sequential write must report correct chunk size")
		totalLen += len(chunk)
	}

	w := httptest.NewRecorder()
	buf.writeTo(w)

	assert.Equal(t, http.StatusOK, w.Code, "status must default to 200 OK")
	assert.Equal(t, "chunk1-chunk2", w.Body.String(), "sequential writes must be concatenated without corruption")
	assert.Equal(t, totalLen, len(w.Body.Bytes()), "total buffer length must equal sum of chunk lengths")
}
