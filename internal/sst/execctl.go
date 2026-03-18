package sst

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type ExecMode int

const (
	ExecCaptured ExecMode = iota
	ExecForeground
)

type ExecRequest struct {
	Cmd  string
	Args []string
	Dir  string
	Env  []string
	Mode ExecMode
}

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

type ExecError struct {
	Cmd      string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *ExecError) Error() string {
	cmdline := e.Cmd
	if len(e.Args) > 0 {
		cmdline += " " + strings.Join(e.Args, " ")
	}
	if e.Stderr != "" {
		return fmt.Sprintf(
			"command failed (%d): %s\n%s",
			e.ExitCode,
			cmdline,
			strings.TrimSpace(e.Stderr),
		)
	}
	return fmt.Sprintf("command failed (%d): %s", e.ExitCode, cmdline)
}

func ExecShell(ctx context.Context, log *Logx, cmd string, args ...string) (*ExecResult, error) {
	return Exec(ctx, log, ExecRequest{
		Cmd:  cmd,
		Args: args,
		Mode: ExecCaptured,
	})
}

func Exec(ctx context.Context, log *Logx, req ExecRequest) (*ExecResult, error) {
	start := time.Now()

	cmdline := req.Cmd
	if len(req.Args) > 0 {
		cmdline += " " + strings.Join(req.Args, " ")
	}

	log.Info(ctx, "exec.start",
		"cmd", req.Cmd,
		"args", req.Args,
		"cmdline", cmdline,
		"mode", req.Mode,
	)

	cmd := exec.Command(req.Cmd, req.Args...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)

	var stdoutBuf, stderrBuf bytes.Buffer

	switch req.Mode {
	case ExecForeground:
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	default:
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	if err := cmd.Start(); err != nil {
		log.Diagnose(ctx, "EXEC_START_FAILED", "failed to start command",
			"cmd", req.Cmd, "args", req.Args, "cmdline", cmdline, "error", err.Error(),
		)
		return nil, err
	}

	waitErr := cmd.Wait()
	dur := time.Since(start)

	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				exitCode = ws.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
	}

	res := &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: dur,
	}

	if exitCode != 0 {
		stderr := strings.TrimSpace(res.Stderr)
		stdout := strings.TrimSpace(res.Stdout)

		errMsg := stderr
		if errMsg == "" && stdout != "" {
			errMsg = stdout
		}

		log.Diagnose(ctx, "EXEC_FAILED", "command execution failed",
			"cmd", req.Cmd, "args", req.Args, "cmdline", cmdline,
			"exit_code", exitCode, "stderr", errMsg, "duration_ms", dur.Milliseconds(),
		)

		if errMsg != "" && req.Mode != ExecForeground {
			fmt.Fprintln(os.Stderr, errMsg)
		}

		return res, &ExecError{
			Cmd:      req.Cmd,
			Args:     req.Args,
			ExitCode: exitCode,
			Stderr:   errMsg,
		}
	}

	log.Info(ctx, "exec.ok", "cmd", req.Cmd, "cmdline", cmdline, "duration_ms", dur.Milliseconds())
	return res, nil
}
