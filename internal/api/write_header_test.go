package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResponseBufferWriteHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{
			name:   "happy path ok",
			status: http.StatusOK,
		},
		{
			name:   "error path bad request",
			status: http.StatusBadRequest,
		},
		{
			name:   "error path not found",
			status: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			buf := newResponseBuffer()
			buf.WriteHeader(tt.status)
			buf.writeTo(w)

			assert.Equal(t, tt.status, w.Code, "responseBuffer WriteHeader must record the status correctly")
		})
	}
}
