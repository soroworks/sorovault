package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSourceGetenv covers the precedence source.Getenv applies: the
// environment first, then the config file, with "" when neither sets it.
// It sets environment variables, so it cannot run in parallel.
func TestSourceGetenv(t *testing.T) {
	const name = "SOROVAULT_TEST_GETENV_KEY"

	tests := []struct {
		name string
		env  string
		file map[string]string
		want string
	}{
		{
			name: "environment value is returned",
			env:  "from-env",
			want: "from-env",
		},
		{
			name: "environment takes precedence over the file",
			env:  "from-env",
			file: map[string]string{"sorovault_test_getenv_key": "from-file"},
			want: "from-env",
		},
		{
			name: "file is used when the environment is unset",
			file: map[string]string{"sorovault_test_getenv_key": "from-file"},
			want: "from-file",
		},
		{
			name: "file keys are matched case-insensitively",
			file: map[string]string{"sorovault_test_getenv_key": "lower"},
			want: "lower",
		},
		{
			name: "missing everywhere yields empty string",
			want: "",
		},
		{
			name: "nil file map with no environment yields empty string",
			file: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(name, tt.env)
			s := &source{file: tt.file}
			assert.Equal(t, tt.want, s.Getenv(name))
		})
	}
}
