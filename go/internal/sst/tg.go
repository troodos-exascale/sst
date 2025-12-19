package sst

import (
	"context"
	"fmt"
	"os"
)

// Result holds the outcome of an Action.
type Result struct {
	Value interface{}
	Error error
}

// Action is a function that performs a single unit of work.
type Action func(ctx context.Context) (interface{}, error)

// Interpreter processes the result of an Action and generates the next steps.
type Interpreter func(result *Result) (continuation []*Context, err error)

// Context defines a single, self-contained node in a task graph.
type Context struct {
	Description      string
	Action           Action
	Interpret        Interpreter
	Cleanup          func()
	InterpretOnError bool
}

// Runner is the engine that executes a graph of Contexts.
type Runner struct {
	Log     Logx
	DryRun  bool
	Verbose bool
	Silent  bool
	Debug   bool
}


// Run is the main entry point for the executor.
func (r *Runner) Run(ctx context.Context, rootCtx *Context) error {
	err := r.runRecursive(ctx, rootCtx)
	if err != nil {
		r.handleError(ctx, err, rootCtx)
	}
	return err
}

func (r *Runner) runRecursive(ctx context.Context, tgCtx *Context) error {
	r.Log.Info(ctx, "Running step", "description", tgCtx.Description)

	actionValue, actionErr := tgCtx.Action(ctx)
	result := &Result{Value: actionValue, Error: actionErr}

	shouldInterpret := (actionErr == nil) || (actionErr != nil && tgCtx.InterpretOnError)

	if tgCtx.Interpret != nil && shouldInterpret {
		continuation, interpErr := tgCtx.Interpret(result)
		if interpErr != nil {
			if !r.Debug && tgCtx.Cleanup != nil {
				r.Log.Info(ctx, "Running cleanup hook", "for_step", tgCtx.Description)
				tgCtx.Cleanup()
			}
			return interpErr
		}

		for _, nextCtx := range continuation {
			if err := r.runRecursive(ctx, nextCtx); err != nil {
				if !r.Debug && tgCtx.Cleanup != nil {
					r.Log.Info(ctx, "Running cleanup hook", "for_step", tgCtx.Description)
					tgCtx.Cleanup()
				}
				return err
			}
		}
	} else if actionErr != nil {
		if !r.Debug && tgCtx.Cleanup != nil {
			r.Log.Info(ctx, "Running cleanup hook", "for_step", tgCtx.Description)
			tgCtx.Cleanup()
		}
		return actionErr
	}

	return nil
}

func (r *Runner) handleError(ctx context.Context, err error, tgCtx *Context) {
	fmt.Fprintf(os.Stderr, "❌ Fatal error during step '%s': %v\n", tgCtx.Description, err)
	r.Log.WithError(err).Error(ctx, "Fatal error in tg runner", "step", tgCtx.Description)
}
