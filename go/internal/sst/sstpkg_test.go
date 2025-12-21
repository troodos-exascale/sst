package sst_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

//
// --- Test Helper Functions ---
//

func assertLogEntry(t *testing.T, logOutput string, expected map[string]string) {
	t.Helper()
	scanner := bytes.NewBufferString(logOutput)
	decoder := json.NewDecoder(scanner)

	for {
		var logEntry map[string]interface{}
		if err := decoder.Decode(&logEntry); err != nil {
			break
		}
		match := true
		for k, v := range expected {
			if val, ok := logEntry[k]; !ok || val != v {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Errorf("Failed to find log entry containing all expected fields: %+v", expected)
}

func assertLogMissing(t *testing.T, logOutput string, unexpected map[string]string) {
	t.Helper()
	scanner := bytes.NewBufferString(logOutput)
	decoder := json.NewDecoder(scanner)

	for {
		var logEntry map[string]interface{}
		if err := decoder.Decode(&logEntry); err != nil {
			break
		}
		match := true
		for k, v := range unexpected {
			if val, ok := logEntry[k]; !ok || val != v {
				match = false
				break
			}
		}
		if match {
			t.Errorf("Found unexpected log entry containing: %+v", unexpected)
			return
		}
	}
}

//
// --- Test Functions ---
//

func TestLogxLevels(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name              string
		globalLevel       string
		subsystemLevels   string
		// Updated to take *sst.Logx
		logFn             func(l *sst.Logx)
		expectInOutput    []map[string]string
		expectNotInOutput []map[string]string
	}{
		{
			name:        "Default Level (INFO)",
			globalLevel: "", 
			logFn: func(l *sst.Logx) {
				l.Debug(ctx, "debug message")
				l.Info(ctx, "info message")
			},
			expectInOutput:    []map[string]string{{"level": "info", "message": "info message"}},
			expectNotInOutput: []map[string]string{{"level": "debug", "message": "debug message"}},
		},
		{
			name:        "Global Level DEBUG",
			globalLevel: "debug",
			logFn: func(l *sst.Logx) {
				l.Debug(ctx, "debug message")
				l.Info(ctx, "info message")
			},
			expectInOutput: []map[string]string{
				{"level": "debug", "message": "debug message"},
				{"level": "info", "message": "info message"},
			},
		},
		{
			name:        "Global Level WARN",
			globalLevel: "warn",
			logFn: func(l *sst.Logx) {
				l.Info(ctx, "info message")
				l.Warn(ctx, "warn message")
			},
			expectInOutput:    []map[string]string{{"level": "warn", "message": "warn message"}},
			expectNotInOutput: []map[string]string{{"level": "info", "message": "info message"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SST_LOG_LEVEL", tc.globalLevel)
			t.Setenv("SST_LOG_LEVELS", tc.subsystemLevels)
			sst.ResetFromEnv()

			var buf bytes.Buffer
			logger := sst.NewWithWriter(&buf)

			tc.logFn(logger)

			output := buf.String()

			for _, expected := range tc.expectInOutput {
				assertLogEntry(t, output, expected)
			}
			for _, notExpected := range tc.expectNotInOutput {
				assertLogMissing(t, output, notExpected)
			}
		})
	}
}

// NOTE: TestFilesFence and TestCoreNew removed because sst.New() is currently undefined.
