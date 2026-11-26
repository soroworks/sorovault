package registry_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// TestNetworkResolvePassphrase covers edge-case passphrases that the main
// Network tests do not exercise: futurenet, a fully custom namespace, and
// an empty string.
func TestNetworkResolvePassphrase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		passphrase string
		want       string
	}{
		{"futurenet passphrase", stellar.NetworkFuturenet, "futurenet"},
		{"custom passphrase is used as-is", "My Private Network ; 2024", "My Private Network ; 2024"},
		{"empty passphrase maps to unknown", "", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := stellartest.New()
			fake.Passphrase = tc.passphrase
			reg := registry.New(fake, spec.XDRDecoder{}, store.NewMemory(), registry.Options{
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})

			got, err := reg.Network(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
