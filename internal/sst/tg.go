package sst

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Result struct {
	Value interface{}
	Error error
}

type Action func(ctx context.Context) (interface{}, error)
type Probe func(ctx context.Context) (bool, error)
type Restore func(ctx context.Context) error
type Interpreter func(result *Result) (continuation []*Context, err error)

type Context struct {
	ID               string
	Description      string
	Probe            Probe
	Restore          Restore
	Action           Action
	Interpret        Interpreter
	Cleanup          func()
	InterpretOnError bool
}

type Runner struct {
	Log     *Logx
	DryRun  bool
	Verbose bool
	Silent  bool
	Debug   bool

	StatePath string
	State     map[string]interface{}
	Completed map[string]bool
}

type persistedGraphState struct {
	Updated   time.Time
	State     map[string]interface{}
	Completed map[string]bool
}

func (r *Runner) LoadState() error {
	if r.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(r.StatePath)
	if err != nil {
		return nil
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

func (r *Runner) SaveState() error {
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

func (r *Runner) Run(ctx context.Context, rootCtx *Context) error {
	if r.State == nil {
		r.State = make(map[string]interface{})
	}
	if r.Completed == nil {
		r.Completed = make(map[string]bool)
	}
	if r.StatePath != "" {
		if err := r.LoadState(); err != nil {
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
	r.Log.Debug(ctx, "step.evaluate", "id", tgCtx.ID, "description", tgCtx.Description)

	var actionValue interface{}
	var actionErr error
	var skipped bool

	if tgCtx.ID != "" && r.Completed[tgCtx.ID] {
		r.Log.Debug(ctx, "step.skipped.cached", "id", tgCtx.ID)
		skipped = true
	} else {
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
			r.Log.Debug(ctx, "step.skipped.probed", "id", tgCtx.ID, "reason", "state matches desired outcome")
			skipped = true
			if tgCtx.ID != "" {
				r.Completed[tgCtx.ID] = true
				_ = r.SaveState()
			}
		} else {
			if tgCtx.Restore != nil {
				r.Log.Debug(ctx, "step.restore.start", "id", tgCtx.ID)
				if err := tgCtx.Restore(ctx); err != nil {
					r.runCleanup(ctx, tgCtx)
					r.Log.WithError(err).Error(ctx, "step.restore.fail", "id", tgCtx.ID)
					return fmt.Errorf("restore failed: %w", err)
				}
			}

			r.Log.Debug(ctx, "step.action.start", "id", tgCtx.ID)
			if tgCtx.Action != nil {
				actionValue, actionErr = tgCtx.Action(ctx)
			}
			
			if actionErr == nil && tgCtx.ID != "" {
				r.Completed[tgCtx.ID] = true
				_ = r.SaveState()
			}
		}
	}

	result := &Result{Value: actionValue, Error: actionErr}
	shouldInterpret := (actionErr == nil) || (tgCtx.InterpretOnError)

	if tgCtx.Interpret != nil && shouldInterpret {
		continuation, interpErr := tgCtx.Interpret(result)
		if interpErr != nil {
			r.runCleanup(ctx, tgCtx)
			return fmt.Errorf("interpret failed: %w", interpErr)
		}

		for _, nextCtx := range continuation {
			if err := r.runRecursive(ctx, nextCtx); err != nil {
				return err
			}
		}
	}

	r.runCleanup(ctx, tgCtx)

	if !skipped && actionErr == nil {
		r.Log.Debug(ctx, "step.action.ok", "id", tgCtx.ID)
	}

	return actionErr
}

func (r *Runner) runCleanup(ctx context.Context, tgCtx *Context) {
	if tgCtx.Cleanup != nil {
		r.Log.Debug(ctx, "step.cleanup.start", "id", tgCtx.ID)
		tgCtx.Cleanup()
		r.Log.Debug(ctx, "step.cleanup.ok", "id", tgCtx.ID)
	}
}

func (r *Runner) handleError(ctx context.Context, err error, tgCtx *Context) {
	r.Log.WithError(err).Error(ctx, "step.action.fail", "id", tgCtx.ID)
}
