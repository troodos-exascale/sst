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
	// We know from the sst tests that NewWithWriter and the level filtering work correctly.
	logger := sst.NewWithWriter(&buf)
	runner := &Runner{
		Log: logger,
	}
	return runner, &buf
}

// TestLoggerWiring is a minimal diagnostic test.
// Its only purpose is to verify that the logger created by newTestRunner
// correctly writes to the in-memory buffer within this package's test.
func TestLoggerWiring(t *testing.T) {
	// Set log level to debug to ensure all messages are captured.
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	runner, logBuf := newTestRunner()
	ctx := context.Background()

	// Do not run the tg executor. Just log a single message directly using the runner's logger.
	runner.Log.Info(ctx, "minimal test message", "key", "value")

	logOutput := logBuf.String()

	// For diagnostics, print the buffer content to the test log.
	// If this test fails, this output will be visible.
	t.Logf("--- RAW LOG BUFFER ---\n%s\n----------------------", logOutput)

	// Assert that the simple message is in the buffer.
	expected := `"message":"minimal test message"`
	if !strings.Contains(logOutput, expected) {
		t.Errorf("Log buffer did not contain the expected message.\nExpected to find: %s", expected)
	}
}
