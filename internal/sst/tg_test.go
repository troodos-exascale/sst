// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
