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

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
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

	var stdoutBuf, stderrBuf bytes.Buffer

	var stdout io.Writer = &stdoutBuf
	var stderr io.Writer = &stderrBuf
	var stdin io.Reader = nil

	if req.Mode == ExecForeground {
		stdin = os.Stdin
		stdout = os.Stdout
		stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	}

	var exitCode int
	var waitErr error

	// Detect if this is a shell invocation (e.g., bash -c '...', sh -c '...')
	isShell := false
	var script string
	var scriptName string
	var scriptArgs []string

	if (req.Cmd == "bash" || req.Cmd == "sh") && len(req.Args) >= 2 && req.Args[0] == "-c" {
		isShell = true
		script = req.Args[1]

		// Map arguments according to bash '$0' semantics
		if len(req.Args) > 2 {
			scriptName = req.Args[2]
		} else {
			scriptName = req.Cmd
		}
		if len(req.Args) > 3 {
			scriptArgs = req.Args[3:]
		}
	}

	if isShell {
		waitErr = executeShell(ctx, log, script, scriptName, scriptArgs, req, stdin, stdout, stderr)
		if waitErr != nil {
			if stat, ok := interp.IsExitStatus(waitErr); ok {
				exitCode = int(stat)
			} else {
				exitCode = 1
			}
		}
	} else {
		waitErr, exitCode = executeBinary(ctx, log, req, stdin, stdout, stderr)
	}

	dur := time.Since(start)

	res := &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: dur,
	}

	if exitCode != 0 || waitErr != nil {
		stderrStr := strings.TrimSpace(res.Stderr)
		stdoutStr := strings.TrimSpace(res.Stdout)

		errMsg := stderrStr
		if errMsg == "" && stdoutStr != "" {
			errMsg = stdoutStr
		}
		if errMsg == "" && waitErr != nil {
			errMsg = waitErr.Error()
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

func executeShell(ctx context.Context, log *Logx, script, scriptName string, scriptArgs []string, req ExecRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	lang := syntax.LangPOSIX
	if req.Cmd == "bash" {
		lang = syntax.LangBash
	}
	
	parser := syntax.NewParser(syntax.Variant(lang))
	prog, err := parser.Parse(strings.NewReader(script), scriptName)
	if err != nil {
		return fmt.Errorf("failed to parse shell script: %w", err)
	}

	// Merge os.Environ and request environment
	envMap := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for _, e := range req.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	var envList []string
	for k, v := range envMap {
		envList = append(envList, k+"="+v)
	}

	// The custom execution handler that wraps the default one to provide granular logging
	execHandler := func(ictx context.Context, args []string) error {
		start := time.Now()
		if len(args) > 0 {
			log.Debug(ctx, "sh.cmd.start", "cmd", args[0], "args", args[1:])
		}

		// Delegate to the actual system execution (0 timeout means no timeout)
		err := interp.DefaultExecHandler(0)(ictx, args)

		dur := time.Since(start)
		if len(args) > 0 {
			if err != nil {
				log.Error(ctx, "sh.cmd.fail", "cmd", args[0], "error", err.Error(), "duration_ms", dur.Milliseconds())
			} else {
				log.Debug(ctx, "sh.cmd.ok", "cmd", args[0], "duration_ms", dur.Milliseconds())
			}
		}
		return err
	}

	runner, err := interp.New(
		interp.Env(expand.ListEnviron(envList...)),
		interp.Dir(req.Dir),
		interp.Params(scriptArgs...),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandler(execHandler),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize shell interpreter: %w", err)
	}

	return runner.Run(ctx, prog)
}

func executeBinary(ctx context.Context, log *Logx, req ExecRequest, stdin io.Reader, stdout, stderr io.Writer) (error, int) {
	cmd := exec.Command(req.Cmd, req.Args...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return err, 1
	}

	waitErr := cmd.Wait()
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
	return waitErr, exitCode
}
