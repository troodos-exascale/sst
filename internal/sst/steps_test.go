package sst_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/swiss-software-tool/sst/internal/sst"
)

func TestSteps_StatePassing(t *testing.T) {
	// Scenario: Step 1 sets a value, Step 2 reads it.

	runner := sst.StepsInit("test-state-passing")

	// Step 1: Produce
	runner.StepsAdd("generate-token", func(ctx *sst.StepsContext, state sst.StepState) error {
		state["token"] = "secret-123"
		return nil
	})

	// Step 2: Consume
	var capturedToken string
	runner.StepsAdd("use-token", func(ctx *sst.StepsContext, state sst.StepState) error {
		if val, ok := state["token"].(string); ok {
			capturedToken = val
		}
		return nil
	})

	if err := sst.StepsRun(runner); err != nil {
		t.Fatalf("StepsRun failed: %v", err)
	}

	if capturedToken != "secret-123" {
		t.Errorf("State propagation failed. Expected 'secret-123', got '%s'", capturedToken)
	}
}

func TestSteps_Persistence(t *testing.T) {
	// Scenario: Run partly, save state, then verify file on disk.

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	runner := sst.StepsInit("test-persistence")
	runner.StepsWithPersistence(stateFile)

	runner.StepsAdd("set-data", func(ctx *sst.StepsContext, state sst.StepState) error {
		state["persisted_key"] = "persisted_value"
		return nil
	})

	if err := sst.StepsRun(runner); err != nil {
		t.Fatalf("StepsRun failed: %v", err)
	}

	// Verify file exists and contains data
	content, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("Failed to read persistence file: %v", err)
	}

	// The saved structure is an internal struct in steps.go, effectively:
	// { "Updated": "...", "State": { ... } }
	var savedData struct {
		State map[string]interface{}
	}
	if err := json.Unmarshal(content, &savedData); err != nil {
		t.Fatalf("Failed to parse persistence file: %v", err)
	}

	if savedData.State["persisted_key"] != "persisted_value" {
		t.Errorf("Persistence failed. File content: %s", string(content))
	}
}

func TestSteps_SkipToStart(t *testing.T) {
	// Scenario: A 3-step pipeline where we resume from Step 2 (index 1).
	// Step 1 should NOT mutate state. Steps 2 and 3 should.

	runner := sst.StepsInit("test-skip-to-start")
	runner.StartIndex = 1 // Skip index 0 (Step 1)

	step1Ran := false
	runner.StepsAdd("step-1-skipped", func(ctx *sst.StepsContext, state sst.StepState) error {
		step1Ran = true
		return nil
	})

	step2Ran := false
	runner.StepsAdd("step-2-executed", func(ctx *sst.StepsContext, state sst.StepState) error {
		step2Ran = true
		return nil
	})

	step3Ran := false
	runner.StepsAdd("step-3-executed", func(ctx *sst.StepsContext, state sst.StepState) error {
		step3Ran = true
		return nil
	})

	if err := sst.StepsRun(runner); err != nil {
		t.Fatalf("StepsRun failed: %v", err)
	}

	if step1Ran {
		t.Error("Step 1 was executed but should have been skipped")
	}
	if !step2Ran {
		t.Error("Step 2 was skipped but should have been executed")
	}
	if !step3Ran {
		t.Error("Step 3 was skipped but should have been executed")
	}
}
