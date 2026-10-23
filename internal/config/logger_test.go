package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()

	// Create a temporary file to use as the logger output
	f, err := os.CreateTemp("", "sorovault-test-log-*")
	require.NoError(t, err)
	defer f.Close()
	defer f.Remove()

	cfg := &config.Config{
		LogLevel: slog.LevelInfo,
	}

	logger := cfg.NewLogger(f)

	// Verify the logger is created and writes to the file
	assert.NotNil(t, logger)

	// Write some test data and verify it's logged
	testMessage := "test log message"
	logger.Info(testMessage)

	// Flush and re-read the file
	f.Sync()
	f.Seek(0, 0)

	data, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	assert.Contains(t, string(data), testMessage)
}