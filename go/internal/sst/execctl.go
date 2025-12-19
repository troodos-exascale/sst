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

// ExecResult captures the outcome of an execution.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// ExecShell executes a shell command with full logx integration.
//
// This is the canonical primitive for external execution.
// Higher-level orchestration (TG, Steps, schedulers) must build on this.
func ExecShell(
	ctx context.Context,
	log Logx,
	name string,
	args ...string,
) (*ExecResult, error) {

	cmdStr := strings.Join(append([]string{name}, args...), " ")

	start := time.Now()
	log.Info(ctx, "exec.start", "command", cmdStr)

	cmd := exec.CommandContext(ctx, name, args...)

	// Propagate environment + STG call path
	cmd.Env = append(os.Environ(), ExportCallPath(ctx)...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	duration := time.Since(start)

	stdout := strings.TrimSpace(stdoutBuf.String())
	stderr := strings.TrimSpace(stderrBuf.String())

	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	res := &ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
		Duration: duration,
	}

	fields := []any{
		"command", cmdStr,
		"exit_code", exitCode,
		"duration", duration.String(),
	}

	if stdout != "" {
		fields = append(fields, "stdout", stdout)
	}
	if stderr != "" {
		fields = append(fields, "stderr", stderr)
	}

	if err != nil {
		log.Error(ctx, "exec.fail", fields...)
		return res, fmt.Errorf("command failed (%d): %s", exitCode, stderr)
	}

	log.Info(ctx, "exec.ok", fields...)
	return res, nil
}
