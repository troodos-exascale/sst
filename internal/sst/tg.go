package sst

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Result holds the outcome of an Action or Probe.
type Result struct {
	Value interface{}
	Error error
}

// Action is a function that performs a single unit of work.
type Action func(ctx context.Context) (interface{}, error)

// Probe evaluates if the intended state already exists in the world.
// Returns true if the state matches the desired outcome (Action can be skipped).
type Probe func(ctx context.Context) (bool, error)

// Restore safely resets the environment to the expected starting state
// (e.g., capturing forensic data, then performing a ZFS rollback).
type Restore func(ctx context.Context) error

// Interpreter processes the result of an Action (or skipped execution)
// and dynamically generates the next steps/capsules.
type Interpreter func(result *Result) (continuation []*Context, err error)

// Context defines a single, self-contained node in a task graph (a "Protosh" capsule).
type Context struct {
	ID               string // Unique identifier for resume/state tracking
	Description      string
	
	Probe            Probe
	Restore          Restore
	Action           Action
	Interpret        Interpreter
	Cleanup          func()
	InterpretOnError bool
}

// Runner is the engine that executes a graph of Contexts.
type Runner struct {
	Log     *Logx
	DryRun  bool
	Verbose bool
	Silent  bool
	Debug   bool

	// State and Resume fields
	StatePath string
	State     map[string]interface{}
	Completed map[string]bool
}

type persistedGraphState struct {
	Updated   time.Time
	State     map[string]interface{}
	Completed map[string]bool
}

func (r *Runner) loadState() error {
	if r.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(r.StatePath)
	if err != nil {
		return nil // Not an error if the state file doesn't exist yet
	}
	var p persistedGraphState
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("load graph state: %w", err)
	}
	if p.State != nil {
		r.State = p.State
	}
	if p.Completed != nil {
		r.Completed = p.Completed
	}
	return nil
}

func (r *Runner) saveState() error {
	if r.StatePath == "" {
		return nil
	}
	p := persistedGraphState{
		Updated:   time.Now().UTC(),
		State:     r.State,
		Completed: r.Completed,
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.StatePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.StatePath, data, 0o644)
}

// Run is the main entry point for the executor.
func (r *Runner) Run(ctx context.Context, rootCtx *Context) error {
	if r.State == nil {
		r.State = make(map[string]interface{})
	}
	if r.Completed == nil {
		r.Completed = make(map[string]bool)
	}
	if r.StatePath != "" {
		if err := r.loadState(); err != nil {
			r.Log.WithError(err).Warn(ctx, "tg.runner.state_load_failed", "path", r.StatePath)
		}
	}

	err := r.runRecursive(ctx, rootCtx)
	if err != nil {
		r.handleError(ctx, err, rootCtx)
	}
	return err
}

func (r *Runner) runRecursive(ctx context.Context, tgCtx *Context) error {
	r.Log.Info(ctx, "step.evaluate", "id", tgCtx.ID, "description", tgCtx.Description)

	var actionValue interface{}
	var actionErr error
	var skipped bool

	// 1. Check Cryptographic/Historical Cache
	if tgCtx.ID != "" && r.Completed[tgCtx.ID] {
		r.Log.Info(ctx, "step.skipped.cached", "id", tgCtx.ID)
		skipped = true
	} else {
		// 2. Real-world state Probe
		var probeMatched bool
		if tgCtx.Probe != nil {
			r.Log.Debug(ctx, "step.probe.start", "id", tgCtx.ID)
			matched, err := tgCtx.Probe(ctx)
			if err != nil {
				r.runCleanup(ctx, tgCtx)
				r.Log.WithError(err).Error(ctx, "step.probe.fail", "id", tgCtx.ID)
				return fmt.Errorf("probe failed: %w", err)
			}
			probeMatched = matched
			r.Log.Debug(ctx, "step.probe.result", "id", tgCtx.ID, "matched", matched)
		}

		if probeMatched {
			r.Log.Info(ctx, "step.skipped.probed", "id", tgCtx.ID, "reason", "state already matches desired outcome")
			skipped = true
			if tgCtx.ID != "" {
				r.Completed[tgCtx.ID] = true
				_ = r.saveState()
			}
		} else {
			// 3. State Reconciliation (Restore)
			if tgCtx.Restore != nil {
				r.Log.Info(ctx, "step.restore.start", "id", tgCtx.ID, "reason", "divergence detected, reconciling environment")
				if err := tgCtx.Restore(ctx); err != nil {
					r.runCleanup(ctx, tgCtx)
					r.Log.WithError(err).Error(ctx, "step.restore.fail", "id", tgCtx.ID)
					return fmt.Errorf("restore failed: %w", err)
				}
			}

			// 4. Execute Action
			r.Log.Info(ctx, "step.action.start", "id", tgCtx.ID)
			if tgCtx.Action != nil {
				actionValue, actionErr = tgCtx.Action(ctx)
			}
			
			if actionErr == nil && tgCtx.ID != "" {
				r.Completed[tgCtx.ID] = true
				_ = r.saveState()
			}
		}
	}

	result := &Result{Value: actionValue, Error: actionErr}
	shouldInterpret := (actionErr == nil) || (tgCtx.InterpretOnError)

	// 5. Interpret (Dynamic Generation of next Protosh capsules)
	if tgCtx.Interpret != nil && shouldInterpret {
		continuation, interpErr := tgCtx.Interpret(result)
		if interpErr != nil {
			r.runCleanup(ctx, tgCtx)
			return fmt.Errorf("interpret failed: %w", interpErr)
		}

		for _, nextCtx := range continuation {
			if err := r.runRecursive(ctx, nextCtx); err != nil {
				r.runCleanup(ctx, tgCtx)
				return err
			}
		}
	} else if actionErr != nil {
		r.runCleanup(ctx, tgCtx)
		r.Log.WithError(actionErr).Error(ctx, "step.action.fail", "id", tgCtx.ID, "description", tgCtx.Description)
		return actionErr
	}

	if !skipped {
		r.Log.Info(ctx, "step.ok", "id", tgCtx.ID, "description", tgCtx.Description)
	}
	return nil
}

func (r *Runner) runCleanup(ctx context.Context, tgCtx *Context) {
	if !r.Debug && tgCtx.Cleanup != nil {
		r.Log.Info(ctx, "step.cleanup", "id", tgCtx.ID, "description", tgCtx.Description)
		tgCtx.Cleanup()
	}
}

func (r *Runner) handleError(ctx context.Context, err error, tgCtx *Context) {
	r.Log.WithError(err).Error(ctx, "tg.runner.fatal", "step", tgCtx.Description)
}
