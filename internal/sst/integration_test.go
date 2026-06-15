package sst

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestEndToEnd_Observability proves that the unified Task Graph (tg.go),
// Linear Steps (steps.go), and AST Shell Executor (execctl.go)
// combine to provide deep, profile-rich, environment-aware observability.
func TestEndToEnd_Observability(t *testing.T) {
	// -------------------------------------------------------------------------
	// PHASE 1: Structured JSON Telemetry (Machine Readable / Profiling)
	// -------------------------------------------------------------------------
	t.Run("JSON_Telemetry", func(t *testing.T) {
		t.Setenv("SST_LOG_FORMAT", "json")
		t.Setenv("SST_LOG_LEVEL", "debug")
		ResetFromEnv()

		var buf bytes.Buffer
		
		// Initialize the pipeline
		pipeline := StepsInit("e2e-profiler")
		
		// Inject our buffered logger into the unexported TG Runner
		pipeline.runner.Log = NewWithWriter(&buf)

		pipeline.StepsAdd("compile-assets", func(ctx *StepsContext, state StepState) error {
			// 1. Log Depth & Namespace Demonstration
			log := pipeline.runner.Log.Subsystem("steps.e2e_profiler.compile_assets")
			log.Info(ctx.Ctx, "preparing to compile", "depth", 3, "module", "core")

			// 2. Shell Boundary Demonstration
			script := `
				set -e
				echo "Starting compilation phase..."
				mkdir -p /tmp/sst_e2e_test_dir
				rm -rf /tmp/sst_e2e_test_dir
				echo "Compilation done."
			`

			_, err := Exec(ctx.Ctx, log, ExecRequest{
				Cmd:  "bash",
				Args: []string{"-c", script},
				Mode: ExecCaptured,
			})
			return err
		})

		// Execute the pipeline
		if err := StepsRun(pipeline); err != nil {
			t.Fatalf("Pipeline failed: %v", err)
		}

		logs := buf.String()
		lines := strings.Split(strings.TrimSpace(logs), "\n")
		
		var foundDepthLog, foundMkdirStart, foundMkdirOk bool

		for _, line := range lines {
			var entry map[string]interface{}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}

			msg, _ := entry["message"].(string)
			subsystem, _ := entry["subsystem"].(string)
			
			// Verify Call Depth (Subsystem) & Custom Fields
			if msg == "preparing to compile" {
				foundDepthLog = true
				if subsystem != "steps.e2e_profiler.compile_assets" {
					t.Errorf("Expected subsystem 'steps.e2e_profiler.compile_assets', got '%s'", subsystem)
				}
				if depth, ok := entry["depth"].(float64); !ok || depth != 3 {
					t.Errorf("Expected depth field = 3, got %v", entry["depth"])
				}
			}

			// Verify Shell Boundary (AST Interception)
			if msg == "sh.cmd.start" {
				if cmd, ok := entry["cmd"].(string); ok && cmd == "mkdir" {
					foundMkdirStart = true
				}
			}

			// Verify Profile Availability (Metrics)
			if msg == "sh.cmd.ok" {
				if cmd, ok := entry["cmd"].(string); ok && cmd == "mkdir" {
					foundMkdirOk = true
					if _, ok := entry["duration_us"]; !ok {
						t.Errorf("Profile missing: Expected 'duration_us' metric on sh.cmd.ok")
					}
				}
			}
			
			// Macro execution profile
			if msg == "exec.ok" {
				if _, ok := entry["duration_us"]; !ok {
					t.Errorf("Profile missing: Expected 'duration_us' metric on exec.ok")
				}
			}
		}

		if !foundDepthLog {
			t.Error("Failed to find the deeply nested context log")
		}
		if !foundMkdirStart || !foundMkdirOk {
			t.Error("Failed to intercept shell boundaries for 'mkdir' AST node")
		}
	})

	// -------------------------------------------------------------------------
	// PHASE 2: Pretty Text Output (Human Readable / Terminal)
	// -------------------------------------------------------------------------
	t.Run("Pretty_Text", func(t *testing.T) {
		t.Setenv("SST_LOG_FORMAT", "text")
		t.Setenv("SST_LOG_LEVEL", "debug")
		ResetFromEnv()

		var buf bytes.Buffer
		
		// Initialize the pipeline
		pipeline := StepsInit("e2e-terminal")
		pipeline.runner.Log = NewWithWriter(&buf)

		pipeline.StepsAdd("run-tests", func(ctx *StepsContext, state StepState) error {
			log := pipeline.runner.Log.Subsystem("steps.e2e_terminal.run_tests")
			
			_, err := Exec(ctx.Ctx, log, ExecRequest{
				Cmd:  "echo",
				Args: []string{"testing terminal output"},
				Mode: ExecCaptured,
			})
			return err
		})

		if err := StepsRun(pipeline); err != nil {
			t.Fatalf("Pipeline failed: %v", err)
		}

		logs := buf.String()
		
		// 1. Verify ANSI color codes are present (\033 or \x1b)
		if !strings.Contains(logs, "\033[") {
			t.Error("Expected ANSI color formatting in text mode, found none.")
		}

		// 2. Verify Subsystem bracket formatting
		if !strings.Contains(logs, "[steps.e2e_terminal.run_tests]") {
			t.Errorf("Expected formatted subsystem brackets, got logs:\n%s", logs)
		}

		// 3. Verify Human-Readable execution translations
		// "exec.ok" -> "✓ done"
		if !strings.Contains(logs, "✓ done") {
			t.Errorf("Expected human-readable '✓ done' execution format, got logs:\n%s", logs)
		}
	})
}
