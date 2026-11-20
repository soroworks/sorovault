package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSourceGetenv(t *testing.T) {
	const name = "SOROVAULT_TEST_GETENV"

	tests := []struct {
		name string
		env  string
		file map[string]string
		want string
	}{
		{name: "environment value", env: "from-env", want: "from-env"},
		{name: "file value when env unset", file: map[string]string{"sorovault_test_getenv": "from-file"}, want: "from-file"},
		{name: "environment wins over file", env: "from-env", file: map[string]string{"sorovault_test_getenv": "from-file"}, want: "from-env"},
		{name: "empty environment falls back to file", env: "", file: map[string]string{"sorovault_test_getenv": "from-file"}, want: "from-file"},
		{name: "unset everywhere is empty", want: ""},
		{name: "nil file map is empty", file: nil, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(name, tc.env)

			s := &source{file: tc.file}
			assert.Equal(t, tc.want, s.Getenv(name))
		})
	}
}
