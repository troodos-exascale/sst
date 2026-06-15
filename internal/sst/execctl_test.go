package sst_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/troodos-exascale/sst/internal/sst"
)

// Helper to assert that a specific sequence of messages exists in the logs
func assertLogContainsMessage(t *testing.T, logs string, expectedMsg string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			if msg, ok := entry["message"].(string); ok && msg == expectedMsg {
				return
			}
		}
	}
	t.Errorf("Expected to find log message '%s', but it was not found. Logs:\n%s", expectedMsg, logs)
}

// Helper to assert that a specific command was logged by the ExecHandler
func assertLogContainsShellCommand(t *testing.T, logs string, status string, expectedCmd string) {
	t.Helper()
	expectedMsg := "sh.cmd." + status
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			if msg, ok := entry["message"].(string); ok && msg == expectedMsg {
				if cmd, ok := entry["cmd"].(string); ok && cmd == expectedCmd {
					return
				}
			}
		}
	}
	t.Errorf("Expected to find shell command log (status: %s) for '%s', but it was not found. Logs:\n%s", status, expectedCmd, logs)
}

func TestExecctl_NativeBinaryExecution(t *testing.T) {
	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()

	// 1. Native binary execution (fallback to standard os/exec)
	req := sst.ExecRequest{
		Cmd:  "echo",
		Args: []string{"native_test"},
		Mode: sst.ExecCaptured,
	}

	res, err := sst.Exec(ctx, log, req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if strings.TrimSpace(res.Stdout) != "native_test" {
		t.Errorf("Expected 'native_test', got: '%s'", res.Stdout)
	}

	logs := logBuf.String()
	assertLogContainsMessage(t, logs, "exec.start")
	assertLogContainsMessage(t, logs, "exec.ok")
}

func TestExecctl_MvdanShellInterpretation(t *testing.T) {
	// Enable DEBUG level so we can see the internal ExecHandler logs
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 2. Shell Script Execution (Parsed via mvdan/sh)
	// 'echo' is a builtin in mvdan/sh, so we use external commands (mkdir, ls) to trigger the ExecHandler
	script := `
	set -e
	echo "hello shell"
	mkdir -p ` + filepath.Join(tmpDir, "test_dir") + `
	ls -d ` + filepath.Join(tmpDir, "test_dir") + `
	`

	req := sst.ExecRequest{
		Cmd:  "bash",
		Args: []string{"-c", script},
		Mode: sst.ExecCaptured,
	}

	res, err := sst.Exec(ctx, log, req)
	if err != nil {
		t.Fatalf("Expected no error executing shell script, got: %v\nStderr: %s", err, res.Stderr)
	}

	stdout := strings.TrimSpace(res.Stdout)
	if !strings.Contains(stdout, "hello shell") || !strings.Contains(stdout, "test_dir") {
		t.Errorf("Unexpected stdout: %s", stdout)
	}

	logs := logBuf.String()

	// Check macro lifecycle logs
	assertLogContainsMessage(t, logs, "exec.start")
	assertLogContainsMessage(t, logs, "exec.ok")

	// Check granular shell interpretation logs intercepted via interp.ExecHandler
	assertLogContainsShellCommand(t, logs, "start", "mkdir")
	assertLogContainsShellCommand(t, logs, "ok", "mkdir")
	assertLogContainsShellCommand(t, logs, "start", "ls")
	assertLogContainsShellCommand(t, logs, "ok", "ls")
}

func TestExecctl_MvdanShellFailure(t *testing.T) {
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()

	// 3. Shell script failure
	// We MUST use an external command to trigger the ExecHandler hook (builtins like 'false' bypass it)
	script := `
	set -e
	echo "running..."
	ls /does-not-exist-123456789
	echo "should not be reached"
	`

	req := sst.ExecRequest{
		Cmd:  "bash",
		Args: []string{"-c", script},
		Mode: sst.ExecCaptured,
	}

	res, err := sst.Exec(ctx, log, req)
	if err == nil {
		t.Fatal("Expected error executing failing shell script, got nil")
	}

	if res.ExitCode == 0 {
		t.Errorf("Expected non-zero exit code, got %d", res.ExitCode)
	}

	logs := logBuf.String()

	// Check macro lifecycle logs ("command execution failed" is the string message)
	assertLogContainsMessage(t, logs, "command execution failed")

	// Check granular shell interpretation logs intercepted via interp.ExecHandler
	assertLogContainsShellCommand(t, logs, "start", "ls")
	assertLogContainsShellCommand(t, logs, "fail", "ls")
}
