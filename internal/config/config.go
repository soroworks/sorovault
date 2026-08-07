// Package config loads SoroVault's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Defaults applied when the corresponding variable is unset.
const (
	DefaultRPCURL            = "https://soroban-testnet.stellar.org"
	DefaultNetworkPassphrase = "Test SDF Network ; September 2015"
	DefaultHTTPAddr          = ":8080"
	DefaultLogLevel          = "info"
	DefaultRPCTimeout        = 30 * time.Second
)

// Config is the resolved runtime configuration.
type Config struct {
	// RPCURL is the Stellar RPC endpoint contract code is read from.
	RPCURL string
	// NetworkPassphrase is the network operators expect RPCURL to serve.
	// It is a guard, not a source of truth: the registry reads the actual
	// passphrase from the endpoint and refuses to run on a mismatch, so a
	// stale RPC_URL cannot file mainnet contracts under "testnet".
	NetworkPassphrase string
	// DatabaseURL is the Postgres connection string for the registry.
	DatabaseURL string
	// HTTPAddr is the address the API and browse UI listen on.
	HTTPAddr string
	// LogLevel is one of debug, info, warn, error.
	LogLevel slog.Level
	// RPCTimeout bounds a single contract fetch.
	RPCTimeout time.Duration
}

// Load reads configuration from the process environment.
//
// DATABASE_URL is the only variable with no usable default, so it is the
// only one whose absence is an error.
func Load() (*Config, error) {
	cfg := &Config{
		RPCURL:            envOr("RPC_URL", DefaultRPCURL),
		NetworkPassphrase: envOr("NETWORK_PASSPHRASE", DefaultNetworkPassphrase),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		HTTPAddr:          envOr("HTTP_ADDR", DefaultHTTPAddr),
		RPCTimeout:        DefaultRPCTimeout,
	}

	level, err := parseLevel(envOr("LOG_LEVEL", DefaultLogLevel))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	if raw := os.Getenv("RPC_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("config: RPC_TIMEOUT %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: RPC_TIMEOUT must be positive, got %s", d)
		}
		cfg.RPCTimeout = d
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("config: DATABASE_URL is required")
	}
	if cfg.RPCURL == "" {
		return nil, errors.New("config: RPC_URL must not be empty")
	}

	return cfg, nil
}

// NewLogger builds the structured logger the rest of the process writes to.
func (c *Config) NewLogger(w *os.File) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: c.LogLevel}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: unknown LOG_LEVEL %q (want debug, info, warn or error)", s)
	}
}
