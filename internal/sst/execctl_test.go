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
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()

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

func TestExecctl_SysHandlerRegistration(t *testing.T) {
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()

	called := false
	sst.RegisterSysHandler("sys.test_cmd", func(ctx context.Context, args []string) error {
		called = true
		return nil
	})

	req := sst.ExecRequest{
		Cmd:  "sys.test_cmd",
		Args: []string{"arg1"},
		Mode: sst.ExecCaptured,
	}

	_, err := sst.Exec(ctx, log, req)
	if err != nil {
		t.Fatalf("sys command failed: %v", err)
	}

	if !called {
		t.Error("sys handler was not called")
	}

	assertLogContainsMessage(t, logBuf.String(), "exec.sys.dispatch")
}

func TestExecctl_MvdanShellInterpretation(t *testing.T) {
	t.Setenv("SST_LOG_LEVEL", "debug")
	sst.ResetFromEnv()

	var logBuf bytes.Buffer
	log := sst.NewWithWriter(&logBuf)
	ctx := context.Background()
	tmpDir := t.TempDir()

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
	assertLogContainsMessage(t, logs, "exec.start")
	assertLogContainsMessage(t, logs, "exec.ok")
	assertLogContainsShellCommand(t, logs, "start", "mkdir")
	assertLogContainsShellCommand(t, logs, "ok", "mkdir")
}
