package sst

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// newTestRunner creates a Runner with a buffered logger for capturing output.
func newTestRunner() (*Runner, *bytes.Buffer) {
	var buf bytes.Buffer
	// NewWithWriter returns *Logx
	logger := NewWithWriter(&buf).Subsystem("test.loggerwiring")
	
	runner := &Runner{
		Log: logger, // Now matches *Logx field in Runner
	}
	return runner, &buf
}

// TestLoggerWiring is a minimal diagnostic test.
func TestLoggerWiring(t *testing.T) {
	t.Setenv("SST_LOG_LEVEL", "debug")
	ResetFromEnv() // Call package-internal function directly

	runner, logBuf := newTestRunner()
	ctx := context.Background()

	// Log a single message directly using the runner's logger.
	runner.Log.Info(ctx, "minimal test message", "key", "value")

	logOutput := logBuf.String()

	t.Logf("--- RAW LOG BUFFER ---\n%s\n----------------------", logOutput)

	expected := `"message":"minimal test message"`
	if !strings.Contains(logOutput, expected) {
		t.Errorf("Log buffer did not contain the expected message.\nExpected to find: %s", expected)
	}
}
