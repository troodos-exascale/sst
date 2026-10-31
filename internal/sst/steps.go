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
	"fmt"
	"os"
	"strings"
)

// StepState is application-owned, serializable state shared between steps.
type StepState map[string]interface{}

// Step represents a single linear unit of work.
type Step struct {
	Name string
	Run  StepFunc
}

// StepFunc is the function executed for a step.
type StepFunc func(ctx *StepsContext, state StepState) error

// StepsContext carries framework-owned, per-step execution metadata.
type StepsContext struct {
	StepName string
	StepIdx  int
	Ctx      context.Context
}

// StepsRunState represents a linear sequence of operations, backed by the TG Runner.
type StepsRunState struct {
	Name       string
	Steps      []Step
	State      StepState
	StartIndex int // Instructs the runner to manually skip to this index (0-based)
	
	runner     *Runner
	statePath  string
	persist    bool
}

// StepsInit initializes a new linear steps execution context.
func StepsInit(name string) *StepsRunState {
	return &StepsRunState{
		Name:  name,
		State: make(StepState),
		runner: &Runner{
			Log:       LogxRoot(),
			State:     make(map[string]interface{}),
			Completed: make(map[string]bool),
		},
	}
}

func (r *StepsRunState) StepsAdd(name string, fn StepFunc) {
	r.Steps = append(r.Steps, Step{
		Name: name,
		Run:  fn,
	})
}

// StepsWithLogger is retained purely as a no-op to prevent compilation errors
// across dozens of callers. Logging is automatically managed by sst.Logx.
func (r *StepsRunState) StepsWithLogger(logf func(format string, args ...any)) {}

func (r *StepsRunState) StepsWithPersistence(path string) {
	r.persist = true
	r.statePath = path
	r.runner.StatePath = path
}

func StepsRun(r *StepsRunState) error {
	return r.execute()
}

func StepsResume(r *StepsRunState) error {
	r.persist = true
	return r.execute()
}

func StepsFinish(r *StepsRunState) error {
	// Could be used to clean up the persistence file if desired.
	return nil
}

func (r *StepsRunState) execute() error {
	if len(r.Steps) == 0 {
		return nil
	}

	// Establish the baseline logging subsystem context.
	runCtx := context.Background()
	runSub := "steps." + sanitizeForNamespace(r.Name)
	r.runner.Log = r.runner.Log.Subsystem(runSub).With("run", r.Name)

	baseCallPath := currentCallPath()
	if baseCallPath == "" {
		baseCallPath = runSub
	} else {
		baseCallPath = baseCallPath + "." + runSub
	}

	// Link application state explicitly into the TG runner's map
	r.runner.State = r.State

	// Pre-fill the completed map if StartIndex is utilized (legacy fast-forward)
	for i := 0; i < r.StartIndex && i < len(r.Steps); i++ {
		r.runner.Completed[r.Steps[i].Name] = true
	}

	// Construct the linked list of Context nodes for the TG runner
	var root *Context
	var prev *Context

	for i, step := range r.Steps {
		stepIdx := i + 1
		stepName := step.Name
		
		curr := &Context{
			ID:          stepName,
			Description: fmt.Sprintf("Step %02d: %s", stepIdx, stepName),
			Action: func(ctx context.Context) (interface{}, error) {
				prevCallPath, hadPrev := os.LookupEnv("SST_CALL_PATH")
				_ = os.Setenv("SST_CALL_PATH", baseCallPath+"."+sanitizeForNamespace(stepName))
				defer func() {
					if hadPrev {
						_ = os.Setenv("SST_CALL_PATH", prevCallPath)
					} else {
						_ = os.Unsetenv("SST_CALL_PATH")
					}
				}()

				sCtx := &StepsContext{
					StepName: stepName,
					StepIdx:  stepIdx,
					Ctx:      ctx,
				}
				
				return nil, step.Run(sCtx, r.State)
			},
		}

		if root == nil {
			root = curr
		}
		
		if prev != nil {
			// Bind the previous step to dynamically return this step
			nextCtx := curr 
			prev.Interpret = func(res *Result) ([]*Context, error) {
				return []*Context{nextCtx}, nil
			}
		}
		prev = curr
	}

	err := r.runner.Run(runCtx, root)
	if err == nil {
		r.runner.Log.Info(runCtx, "steps.run.ok")
	}
	return err
}

func currentCallPath() string {
	if v, ok := os.LookupEnv("SST_CALL_PATH"); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func sanitizeForNamespace(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "step"
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()

	out = strings.Trim(out, "._-")
	if out == "" {
		return "step"
	}
	return out
}
