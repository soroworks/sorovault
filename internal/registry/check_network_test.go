package registry_test

import (
	"context"
	"errors"
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

func TestCheckNetwork(t *testing.T) {
	t.Parallel()

	rpcDown := errors.New("connection refused")

	tests := []struct {
		name       string
		passphrase string // what the endpoint reports
		expect     string // what the operator configured
		networkErr error
		wantErr    error
	}{
		{name: "matching passphrase", passphrase: stellar.NetworkTestnet, expect: stellar.NetworkTestnet},
		{name: "no expectation accepts any network", passphrase: stellar.NetworkPublic},
		{name: "mismatched passphrase", passphrase: stellar.NetworkPublic, expect: stellar.NetworkTestnet, wantErr: registry.ErrNetworkMismatch},
		{name: "unreachable endpoint", expect: stellar.NetworkTestnet, networkErr: rpcDown, wantErr: rpcDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := stellartest.New()
			if tt.passphrase != "" {
				fake.Passphrase = tt.passphrase
			}
			fake.NetworkErr = tt.networkErr

			reg := registry.New(fake, spec.XDRDecoder{}, store.NewMemory(), registry.Options{
				ExpectPassphrase: tt.expect,
				Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
			})

			err := reg.CheckNetwork(context.Background())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestCheckNetworkBypassesCache checks that readiness probes hit the endpoint
// every time, so an endpoint that goes away after startup is noticed.
func TestCheckNetworkBypassesCache(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	reg := registry.New(fake, spec.XDRDecoder{}, store.NewMemory(), registry.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	_, err := reg.Network(context.Background())
	require.NoError(t, err)

	fake.NetworkErr = errors.New("connection refused")
	require.Error(t, reg.CheckNetwork(context.Background()))
	assert.Equal(t, 2, fake.Calls["Network"])
}
