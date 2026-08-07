package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
)

const testDatabaseURL = "postgres://user:pass@localhost:5432/sorovault"

// setEnv applies a set of variables for one test, clearing any others the
// package reads so the ambient environment cannot influence the result.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()

	for _, key := range []string{
		"RPC_URL", "NETWORK_PASSPHRASE", "DATABASE_URL",
		"HTTP_ADDR", "LOG_LEVEL", "RPC_TIMEOUT",
	} {
		if v, ok := vars[key]; ok {
			t.Setenv(key, v)
			continue
		}
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL})

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
	assert.Equal(t, config.DefaultNetworkPassphrase, cfg.NetworkPassphrase)
	assert.Equal(t, config.DefaultHTTPAddr, cfg.HTTPAddr)
	assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, config.DefaultRPCTimeout, cfg.RPCTimeout)
}

func TestLoadOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":       testDatabaseURL,
		"RPC_URL":            "https://rpc.example.test",
		"NETWORK_PASSPHRASE": "Public Global Stellar Network ; September 2015",
		"HTTP_ADDR":          "127.0.0.1:9999",
		"LOG_LEVEL":          "debug",
		"RPC_TIMEOUT":        "90s",
	})

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "https://rpc.example.test", cfg.RPCURL)
	assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
	assert.Equal(t, "127.0.0.1:9999", cfg.HTTPAddr)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, 90*time.Second, cfg.RPCTimeout)
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setEnv(t, nil)

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLogLevels(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
		{"  Info  ", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": tt.in})

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogLevel)
		})
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string
	}{
		{
			name:    "unknown log level",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": "chatty"},
			wantErr: "unknown LOG_LEVEL",
		},
		{
			name:    "unparseable timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "soon"},
			wantErr: "RPC_TIMEOUT",
		},
		{
			name:    "non-positive timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "negative timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "-5s"},
			wantErr: "must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.vars)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
