package sst

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

//
// =========================
// Exported Steps API (verbs)
// =========================
//
// These are the ONLY exported entry points of the steps framework.
// Anyone reading this file should immediately know what is available
// and where to find it.
//

// StepsInit initializes a new linear steps execution context.
func StepsInit(name string) *StepsRunState {
	return &StepsRunState{
		Name:  name,
		State: make(StepState),
		logf:  func(string, ...any) {},
	}
}

// StepsRun executes all registered steps in order.
func StepsRun(r *StepsRunState) error {
	return r.runAll()
}

// StepsResume resumes execution from persisted state.
func StepsResume(r *StepsRunState) error {
	r.persist = true
	return r.runAll()
}

// StepsFinish finalizes a steps run (currently a no-op, reserved).
func StepsFinish(r *StepsRunState) error {
	return nil
}

//
// =========================
// Nouns: state & structure
// =========================

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
}

// StepsRunState is the superblock / inode of a steps execution.
type StepsRunState struct {
	Name      string
	Steps     []Step
	State     StepState
	persist   bool
	statePath string
	logf      func(format string, args ...any)
}

//
// =========================
// Public methods on state
// =========================

// StepsAdd registers a new step.
func (r *StepsRunState) StepsAdd(name string, fn StepFunc) {
	r.Steps = append(r.Steps, Step{
		Name: name,
		Run:  fn,
	})
}

// StepsWithLogger attaches a logger to the run.
func (r *StepsRunState) StepsWithLogger(logf func(format string, args ...any)) {
	r.logf = logf
}

// StepsWithPersistence enables persistence to the given path.
func (r *StepsRunState) StepsWithPersistence(path string) {
	r.persist = true
	r.statePath = path
}

//
// =========================
// Private execution logic
// =========================

func (r *StepsRunState) runAll() error {
	if r.persist {
		_ = r.loadState()
	}

	for i, step := range r.Steps {
		ctx := &StepsContext{
			StepName: step.Name,
			StepIdx:  i + 1,
		}

		r.logf("STEP %02d %s", ctx.StepIdx, ctx.StepName)

		if err := step.Run(ctx, r.State); err != nil {
			r.logf("FAIL %02d %s: %v", ctx.StepIdx, ctx.StepName, err)
			return err
		}

		if r.persist {
			if err := r.saveState(); err != nil {
				return err
			}
		}

		r.logf("OK   %02d %s", ctx.StepIdx, ctx.StepName)
	}

	return nil
}

//
// =========================
// Persistence (private)
// =========================

type persistedStepsState struct {
	Updated time.Time
	State   StepState
}

func (r *StepsRunState) loadState() error {
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		return nil
	}

	var p persistedStepsState
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("load steps state: %w", err)
	}
	r.State = p.State
	return nil
}

func (r *StepsRunState) saveState() error {
	p := persistedStepsState{
		Updated: time.Now().UTC(),
		State:   r.State,
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.statePath, data, 0o644)
}
