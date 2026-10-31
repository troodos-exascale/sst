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
	"context"
	"errors"
	"testing"
)

func TestTG_BranchingWorkflow(t *testing.T) {
	runner, _ := newTestRunner() // Reusing helper from tg_test.go
	ctx := context.Background()

	var executionLog []string

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

	rootTask := &Context{
		Description: "Check Resource",
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "check")
			return true, nil // Simulate Exists = true
		},
		Interpret: func(res *Result) ([]*Context, error) {
			exists := res.Value.(bool)
			if exists {
				return []*Context{taskDelete}, nil
			}
			return []*Context{taskCreate}, nil
		},
	}

	if err := runner.Run(ctx, rootTask); err != nil {
		t.Fatalf("Runner failed: %v", err)
	}

	if len(executionLog) != 2 || executionLog[0] != "check" || executionLog[1] != "delete" {
		t.Fatalf("Expected check -> delete, got %v", executionLog)
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
	if !cleanupCalled {
		t.Error("Cleanup hook was NOT called on failure")
	}
}

func TestTG_ProbeAndRestoreLifecycle(t *testing.T) {
	runner, _ := newTestRunner()
	ctx := context.Background()

	var executionLog []string

	rootTask := &Context{
		ID:          "test-node-1",
		Description: "Test Probe and Restore",
		Probe: func(ctx context.Context) (bool, error) {
			executionLog = append(executionLog, "probe")
			return false, nil // returning false forces Restore -> Action
		},
		Restore: func(ctx context.Context) error {
			executionLog = append(executionLog, "restore")
			return nil
		},
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "action")
			return "done", nil
		},
		Interpret: func(res *Result) ([]*Context, error) {
			executionLog = append(executionLog, "interpret")
			return nil, nil
		},
	}

	if err := runner.Run(ctx, rootTask); err != nil {
		t.Fatalf("Runner failed: %v", err)
	}

	expectedLog := []string{"probe", "restore", "action", "interpret"}
	if len(executionLog) != len(expectedLog) {
		t.Fatalf("Expected execution log %v, got %v", expectedLog, executionLog)
	}
	for i, v := range expectedLog {
		if executionLog[i] != v {
			t.Errorf("At index %d expected %s, got %s", i, v, executionLog[i])
		}
	}
}

func TestTG_ProbeSkipsAction(t *testing.T) {
	runner, _ := newTestRunner()
	ctx := context.Background()

	var executionLog []string

	rootTask := &Context{
		ID:          "test-node-2",
		Description: "Test Probe Skip",
		Probe: func(ctx context.Context) (bool, error) {
			executionLog = append(executionLog, "probe")
			return true, nil // true should SKIP Restore and Action
		},
		Restore: func(ctx context.Context) error {
			executionLog = append(executionLog, "restore")
			return nil
		},
		Action: func(ctx context.Context) (interface{}, error) {
			executionLog = append(executionLog, "action")
			return "done", nil
		},
		Interpret: func(res *Result) ([]*Context, error) {
			executionLog = append(executionLog, "interpret")
			return nil, nil
		},
	}

	if err := runner.Run(ctx, rootTask); err != nil {
		t.Fatalf("Runner failed: %v", err)
	}

	expectedLog := []string{"probe", "interpret"}
	if len(executionLog) != len(expectedLog) {
		t.Fatalf("Expected execution log %v, got %v", expectedLog, executionLog)
	}
	for i, v := range expectedLog {
		if executionLog[i] != v {
			t.Errorf("At index %d expected %s, got %s", i, v, executionLog[i])
		}
	}
}
