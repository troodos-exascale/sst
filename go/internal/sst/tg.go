package sst

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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

// Command is a helper that creates an Action for running an external shell command.
func (r *Runner) Command(name string, args ...string) Action {
	return func(ctx context.Context) (interface{}, error) {
		cmdStr := fmt.Sprintf("%s %s", name, strings.Join(args, " "))

		if r.DryRun {
			// In DryRun, console output is always printed, ignoring Silent.
			fmt.Fprintf(os.Stderr, "[DRY-RUN] %s\n", cmdStr)
			r.Log.Info(ctx, "Dry run", "command", cmdStr)
			return "", nil // Return success for dry runs to allow graph traversal.
		}

		isSilent := r.Silent
		if r.Debug {
			isSilent = false // Debug mode forces console output.
		}

		if !isSilent {
			fmt.Fprintf(os.Stderr, "==> EXEC: %s\n", cmdStr)
		}

		r.Log.Info(ctx, "Executing command", "command", cmdStr)
		start := time.Now()

		cmd := exec.CommandContext(ctx, name, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		duration := time.Since(start)

		stdoutStr := strings.TrimSpace(stdout.String())
		stderrStr := strings.TrimSpace(stderr.String())

		exitCode := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				exitCode = exitError.ExitCode()
			} else {
				exitCode = -1 // Indicates a non-exit error (e.g., command not found).
			}
		}

		logFields := []interface{}{
			"command", cmdStr, "duration", duration.String(), "exit_code", exitCode,
		}
		if stderrStr != "" {
			logFields = append(logFields, "stderr", stderrStr)
		}
		isVerbose := r.Verbose || r.Debug
		if isVerbose && stdoutStr != "" {
			logFields = append(logFields, "stdout", stdoutStr)
		}

		if err != nil {
			r.Log.Error(ctx, "Command finished with an error", logFields...)
			return stdoutStr, fmt.Errorf("command failed with exit code %d: %s", exitCode, stderrStr)
		}

		r.Log.Info(ctx, "Command successful", logFields...)
		return stdoutStr, nil
	}
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
