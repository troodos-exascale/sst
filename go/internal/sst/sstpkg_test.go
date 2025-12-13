package sst_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

//
// --- Test Helper Functions ---
//

// assertLogEntry checks if at least one JSON line in the log buffer contains all the key-value pairs from the expected map.
func assertLogEntry(t *testing.T, logOutput string, expected map[string]string) {
	t.Helper()
	scanner := bytes.NewBufferString(logOutput)
	decoder := json.NewDecoder(scanner)

	for {
		var logEntry map[string]interface{}
		if err := decoder.Decode(&logEntry); err != nil {
			break // End of log buffer
		}

		match := true
		for k, v := range expected {
			if val, ok := logEntry[k]; !ok || val != v {
				match = false
				break
			}
		}

		if match {
			return // Found a matching log entry
		}
	}

	// If we get here, no log entry matched
	t.Errorf("Failed to find log entry containing all expected fields: %+v", expected)
	t.Logf("Full log output:\n%s", logOutput)
}

// assertLogMissing asserts that no JSON line in the log buffer contains the specified key-value pair.
func assertLogMissing(t *testing.T, logOutput string, unexpected map[string]string) {
	t.Helper()
	scanner := bytes.NewBufferString(logOutput)
	decoder := json.NewDecoder(scanner)

	for {
		var logEntry map[string]interface{}
		if err := decoder.Decode(&logEntry); err != nil {
			break // End of log buffer
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
			t.Logf("Full log output:\n%s", logOutput)
			return
		}
	}
}

//
// --- Test Functions ---
//

func TestCoreNew(t *testing.T) {
	// Create a temporary directory and initialize a git repo in it.
	tmpDir, err := os.MkdirTemp("", "sst-test-new-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	// CORRECTED: Check error on deferred cleanup.
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if _, err := git.PlainInit(tmpDir, false); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get wd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	// CORRECTED: Check error on deferred cleanup.
	defer func() {
		if err := os.Chdir(originalWd); err != nil {
			t.Fatalf("Failed to restore original working directory: %v", err)
		}
	}()

	// Now New() should succeed.
	_, err = sst.New()
	if err != nil {
		t.Fatalf("sst.New() failed inside a valid git repo: %v", err)
	}
}

func TestLogxLevels(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name              string
		globalLevel       string
		subsystemLevels   string
		logFn             func(l sst.Logx)
		expectInOutput    []map[string]string
		expectNotInOutput []map[string]string
	}{
		{
			name:        "Default Level (INFO)",
			globalLevel: "", // Defaults to INFO
			logFn: func(l sst.Logx) {
				l.Debug(ctx, "debug message")
				l.Info(ctx, "info message")
			},
			expectInOutput:    []map[string]string{{"level": "info", "message": "info message"}},
			expectNotInOutput: []map[string]string{{"level": "debug", "message": "debug message"}},
		},
		{
			name:        "Global Level DEBUG",
			globalLevel: "debug",
			logFn: func(l sst.Logx) {
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
			logFn: func(l sst.Logx) {
				l.Info(ctx, "info message")
				l.Warn(ctx, "warn message")
			},
			expectInOutput:    []map[string]string{{"level": "warn", "message": "warn message"}},
			expectNotInOutput: []map[string]string{{"level": "info", "message": "info message"}},
		},
		{
			name:            "Subsystem Override",
			globalLevel:     "info",
			subsystemLevels: "test-sub=debug",
			logFn: func(l sst.Logx) {
				l.Debug(ctx, "global debug")                          // Should be dropped
				l.Subsystem("test-sub").Debug(ctx, "subsystem debug") // Should be kept
			},
			expectInOutput:    []map[string]string{{"subsystem": "test-sub", "message": "subsystem debug"}},
			expectNotInOutput: []map[string]string{{"message": "global debug"}},
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

func TestFilesFence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sst-test-repo-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	// CORRECTED: Check error on deferred cleanup.
	defer func() { _ = os.RemoveAll(tmpDir) }()

	gitCmds := [][]string{
		{"init"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"commit", "--allow-empty", "-m", "Initial commit"},
	}
	for _, args := range gitCmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to run git command 'git %s': %v", strings.Join(args, " "), err)
		}
	}

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get wd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	// CORRECTED: Check error on deferred cleanup.
	defer func() {
		if err := os.Chdir(originalWd); err != nil {
			t.Fatalf("Failed to restore original working directory: %v", err)
		}
	}()

	core, err := sst.New()
	if err != nil {
		t.Fatalf("sst.New() failed inside a valid git repo: %v", err)
	}
	fencer := core.Files()

	expectedRoot, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks for expected root: %v", err)
	}

	if fencer.Root() != expectedRoot {
		t.Errorf("Expected fencer root to be '%s', got '%s'", expectedRoot, fencer.Root())
	}

	testCases := []struct {
		name      string
		path      string
		expectErr bool
	}{
		{"Valid relative path", "components/s2/svc/my-svc", false},
		{"Valid absolute path", filepath.Join(expectedRoot, "projects/s2/m1"), false},
		{"Path traversal up", "../some/other/dir", true},
		{"Absolute path outside root", "/etc/passwd", true},
		{"Path inside .git", ".git/config", true},
		{"Empty path", "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fencer.Fence(tc.path)
			if tc.expectErr && err == nil {
				t.Errorf("Expected an error for path '%s', but got none", tc.path)
			}
			if !tc.expectErr && err != nil {
				t.Errorf("Expected no error for path '%s', but got: %v", tc.path, err)
			}
		})
	}
}
