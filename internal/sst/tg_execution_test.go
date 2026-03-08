package sst

import (
	"context"
	"errors"
	"testing"
)

func TestTG_BranchingWorkflow(t *testing.T) {
	// Scenario: Check if a "file" exists. If yes, delete it. If no, create it.
	// This tests Action -> Interpret -> Branching logic.

	runner, _ := newTestRunner() // Reusing helper from tg_test.go (now visible)
	ctx := context.Background()

	var executionLog []string

	// Define the leaf tasks
	taskCreate := &Context{
		Description: "Create Resource",
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "create")
			return "created", nil
		},
	}

	taskDelete := &Context{
		Description: "Delete Resource",
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "delete")
			return "deleted", nil
		},
	}

	// Define the root decision task
	rootTask := &Context{
		Description: "Check Resource",
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "check")
			// Simulate "Exists = true"
			return true, nil
		},
		Interpret: func(res *Result) ([]*Context, error) {
			exists := res.Value.(bool)
			if exists {
				// Branch A: Resource exists -> Delete it
				return []*Context{taskDelete}, nil
			}
			// Branch B: Resource missing -> Create it
			return []*Context{taskCreate}, nil
		},
	}

	// Run
	if err := runner.Run(ctx, rootTask); err != nil {
		t.Fatalf("Runner failed: %v", err)
	}

	// Verify Execution Order
	if len(executionLog) != 2 {
		t.Fatalf("Expected 2 steps, got %d", len(executionLog))
	}
	if executionLog[0] != "check" {
		t.Errorf("Expected first step 'check', got '%s'", executionLog[0])
	}
	if executionLog[1] != "delete" {
		t.Errorf("Expected second step 'delete', got '%s'", executionLog[1])
	}
}

func TestTG_ErrorHandlingAndCleanup(t *testing.T) {
	runner, _ := newTestRunner()
	ctx := context.Background()

	cleanupCalled := false

	rootTask := &Context{
		Description: "Task with Error",
		Action: func(ctx context.Context) (interface{}, error) {
			return nil, errors.New("simulated failure")
		},
		Cleanup: func() {
			cleanupCalled = true
		},
	}

	err := runner.Run(ctx, rootTask)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if err.Error() != "simulated failure" {
		t.Errorf("Expected 'simulated failure', got '%v'", err)
	}
	if !cleanupCalled {
		t.Error("Cleanup hook was NOT called on failure")
	}
}
