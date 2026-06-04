package sst

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
func StepsFinish(r *StepsRunState) error { return nil }

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

	// Ctx is a context for structured logging/tracing.
	// Callers may ignore it; existing code remains valid.
	Ctx context.Context
}

// StepsRunState is the superblock / inode of a steps execution.
type StepsRunState struct {
	Name       string
	Steps      []Step
	State      StepState
	StartIndex int // Instructs the runner to fast-forward to this index (0-based)
	persist    bool
	statePath  string

	// Legacy logger callback (kept for compatibility).
	// New structured logging uses LogxRoot() internally.
	logf func(format string, args ...any)
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

// StepsWithLogger attaches a legacy formatter logger to the run.
// Kept for compatibility: structured logging is handled via LogxRoot().
func (r *StepsRunState) StepsWithLogger(logf func(format string, args ...any)) {
	if logf == nil {
		r.logf = func(string, ...any) {}
		return
	}
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

	// Base logging context for this run.
	runCtx := context.Background()
	runSub := "steps." + sanitizeForNamespace(r.Name)
	runLog := LogxRoot().Subsystem(runSub).With("run", r.Name)

	// Establish a base call path for downstream processes.
	// We do not assume logx consumes env yet; we only emit it.
	baseCallPath := currentCallPath()
	if baseCallPath == "" {
		baseCallPath = runSub
	} else {
		baseCallPath = baseCallPath + "." + runSub
	}

	for i, step := range r.Steps {
		stepName := step.Name
		stepIdx := i + 1

		stepSub := runSub + "." + sanitizeForNamespace(stepName)
		stepLog := LogxRoot().Subsystem(stepSub).With("run", r.Name, "step", stepName, "step_idx", stepIdx)

		// Fast-forward mechanic for resumable builds
		if i < r.StartIndex {
			r.logf("SKIP %02d %s (Cached)", stepIdx, stepName)
			stepLog.Info(runCtx, "step.skipped.cached")
			continue
		}

		// Set SST_CALL_PATH for the duration of this step.
		prevCallPath, hadPrev := os.LookupEnv("SST_CALL_PATH")
		_ = os.Setenv("SST_CALL_PATH", baseCallPath+"."+sanitizeForNamespace(stepName))

		ctx := &StepsContext{
			StepName: stepName,
			StepIdx:  stepIdx,
			Ctx:      runCtx,
		}

		// Step start (legacy + structured)
		r.logf("STEP %02d %s", stepIdx, stepName)
		stepLog.Info(runCtx, "step.start")

		err := step.Run(ctx, r.State)

		if err != nil {
			// Failure marking is handled by structured logging.
			stepLog.WithError(err).Error(runCtx, "step.fail")
			r.logf("FAIL %02d %s: %v", stepIdx, stepName, err)

			// Restore prior call path before returning.
			if hadPrev {
				_ = os.Setenv("SST_CALL_PATH", prevCallPath)
			} else {
				_ = os.Unsetenv("SST_CALL_PATH")
			}
			return err
		}

		if r.persist {
			if err := r.saveState(); err != nil {
				stepLog.WithError(err).Error(runCtx, "steps.persist.fail")
				r.logf("FAIL %02d %s: %v", stepIdx, stepName, err)

				// Restore prior call path before returning.
				if hadPrev {
					_ = os.Setenv("SST_CALL_PATH", prevCallPath)
				} else {
					_ = os.Unsetenv("SST_CALL_PATH")
				}
				return err
			}
		}

		stepLog.Info(runCtx, "step.ok")
		r.logf("OK   %02d %s", stepIdx, stepName)

		// Restore prior call path after successful step.
		if hadPrev {
			_ = os.Setenv("SST_CALL_PATH", prevCallPath)
		} else {
			_ = os.Unsetenv("SST_CALL_PATH")
		}
	}

	runLog.Info(runCtx, "steps.run.ok")
	return nil
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

	// Keep [a-z0-9._-], replace others with '_'.
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

	// Avoid empty/degenerate segments.
	out = strings.Trim(out, "._-")
	if out == "" {
		return "step"
	}
	return out
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
