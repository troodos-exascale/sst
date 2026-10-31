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
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestEndToEnd_Observability(t *testing.T) {
	// Image building and shell observability tests require native Linux features
	// and cannot be cleanly executed on macOS due to path and shell version differences.
	if runtime.GOOS != "linux" {
		t.Skip("Skipping integration test: observability requirements are native to Linux")
	}

	// -------------------------------------------------------------------------
	// PHASE 1: Structured JSON Telemetry
	// -------------------------------------------------------------------------
	t.Run("JSON_Telemetry", func(t *testing.T) {
		t.Setenv("SST_LOG_FORMAT", "json")
		t.Setenv("SST_LOG_LEVEL", "debug")
		ResetFromEnv()

		var buf bytes.Buffer
		pipeline := StepsInit("e2e-profiler")
		pipeline.runner.Log = NewWithWriter(&buf)

		pipeline.StepsAdd("compile-assets", func(ctx *StepsContext, state StepState) error {
			log := pipeline.runner.Log.Subsystem("steps.e2e-profiler.compile-assets")
			log.Info(ctx.Ctx, "preparing to compile", "depth", 3)

			script := `
				set -e
				mkdir -p /tmp/sst-e2e-test
				rm -rf /tmp/sst-e2e-test
			`

			_, err := Exec(ctx.Ctx, log, ExecRequest{
				Cmd:  "bash",
				Args: []string{"-c", script},
				Mode: ExecCaptured,
			})
			return err
		})

		if err := StepsRun(pipeline); err != nil {
			t.Fatalf("Pipeline failed: %v", err)
		}

		logs := buf.String()
		lines := strings.Split(strings.TrimSpace(logs), "\n")
		
		var foundDepthLog, foundMkdirOk bool

		for _, line := range lines {
			var entry map[string]interface{}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}

			msg, _ := entry["message"].(string)
			subsystem, _ := entry["subsystem"].(string)
			
			if msg == "preparing to compile" {
				foundDepthLog = true
				if subsystem != "steps.e2e-profiler.compile-assets" {
					t.Errorf("Expected subsystem 'steps.e2e-profiler.compile-assets', got '%s'", subsystem)
				}
			}

			if msg == "sh.cmd.ok" {
				if cmd, ok := entry["cmd"].(string); ok && cmd == "mkdir" {
					foundMkdirOk = true
					if _, ok := entry["duration_us"]; !ok {
						t.Errorf("Metric missing: duration_us for mkdir")
					}
				}
			}
		}

		if !foundDepthLog { t.Error("Depth log missing") }
		if !foundMkdirOk { t.Error("Shell boundary interception failed") }
	})

	// -------------------------------------------------------------------------
	// PHASE 2: Pretty Text Output
	// -------------------------------------------------------------------------
	t.Run("Pretty_Text", func(t *testing.T) {
		t.Setenv("SST_LOG_FORMAT", "text")
		t.Setenv("SST_LOG_LEVEL", "debug")
		ResetFromEnv()

		var buf bytes.Buffer
		pipeline := StepsInit("e2e-terminal")
		pipeline.runner.Log = NewWithWriter(&buf)

		pipeline.StepsAdd("run-tests", func(ctx *StepsContext, state StepState) error {
			log := pipeline.runner.Log.Subsystem("steps.e2e-terminal.run-tests")
			_, err := Exec(ctx.Ctx, log, ExecRequest{
				Cmd:  "echo",
				Args: []string{"test"},
				Mode: ExecCaptured,
			})
			return err
		})

		_ = StepsRun(pipeline)
		logs := buf.String()
		
		if !strings.Contains(logs, "\033[") { t.Error("ANSI missing") }
		if !strings.Contains(logs, "[steps.e2e-terminal.run-tests]") { t.Error("Subsystem formatting wrong") }
		if !strings.Contains(logs, "✓ done") { t.Error("Human-readable translation missing") }
	})
}
