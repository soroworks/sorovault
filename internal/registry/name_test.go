package registry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/store"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "empty means no name", in: "", want: ""},
		{name: "only whitespace means no name", in: " \t ", want: ""},
		{name: "surrounding whitespace is trimmed", in: "  Example Token  ", want: "Example Token"},
		{name: "inner spaces are kept", in: "Example   Token", want: "Example   Token"},
		{name: "exactly the limit", in: strings.Repeat("a", registry.MaxNameLength), want: strings.Repeat("a", registry.MaxNameLength)},
		{name: "the limit counts characters, not bytes", in: strings.Repeat("ü", registry.MaxNameLength), want: strings.Repeat("ü", registry.MaxNameLength)},
		{name: "one over the limit", in: strings.Repeat("a", registry.MaxNameLength+1), wantErr: "longer than 100 characters"},
		{name: "the limit applies after trimming", in: " " + strings.Repeat("a", registry.MaxNameLength) + " ", want: strings.Repeat("a", registry.MaxNameLength)},
		{name: "an inner newline is a control character", in: "two\nlines", wantErr: "control characters"},
		{name: "NUL is a control character", in: "nul\x00", wantErr: "control characters"},
		{name: "DEL is a control character", in: "del\x7f", wantErr: "control characters"},
		{name: "invalid UTF-8", in: "bad\xff", wantErr: "not valid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := registry.NormalizeName(tt.in)
			if tt.wantErr != "" {
				require.ErrorIs(t, err, registry.ErrInvalidName)
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegisterNamedAndRename(t *testing.T) {
	t.Parallel()

	reg, fake, _ := newRegistry(t)
	fake.Deploy(idA, moduleWith(t, "hello"))
	ctx := context.Background()

	result, err := reg.RegisterNamed(ctx, idA, " Example Token ")
	require.NoError(t, err)
	assert.Equal(t, "Example Token", result.Contract.Name)

	_, err = reg.RegisterNamed(ctx, idA, strings.Repeat("a", registry.MaxNameLength+1))
	require.ErrorIs(t, err, registry.ErrInvalidName)

	// Refreshing an upgraded contract keeps the name.
	fake.Deploy(idA, moduleWith(t, "goodbye"))
	result, err = reg.Refresh(ctx, idA)
	require.NoError(t, err)
	require.True(t, result.Changed)
	assert.Equal(t, "Example Token", result.Contract.Name)

	contract, err := reg.Rename(ctx, idA, "Renamed")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", contract.Name)

	_, err = reg.Rename(ctx, idA, "  ")
	require.ErrorIs(t, err, registry.ErrInvalidName, "renaming to nothing is refused")

	_, err = reg.Rename(ctx, idB, "Unregistered")
	require.ErrorIs(t, err, store.ErrNotFound)
}
