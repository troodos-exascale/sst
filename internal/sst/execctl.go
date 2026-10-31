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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Registry for Go-native "sys." commands
var (
	sysMu       sync.RWMutex
	sysHandlers = make(map[string]func(context.Context, []string) error)
)

// RegisterSysHandler allows external packages (like rtsst) to hook Go logic into the Exec engine.
func RegisterSysHandler(name string, fn func(context.Context, []string) error) {
	sysMu.Lock()
	defer sysMu.Unlock()
	sysHandlers[name] = fn
}

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
		return fmt.Sprintf("command failed (%d): %s\nReason: %s", e.ExitCode, cmdline, strings.TrimSpace(e.Stderr))
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
	log.Debug(ctx, "exec.start", "cmd", req.Cmd)

	// 1. Check Global Sys Registry
	sysMu.RLock()
	handler, isSys := sysHandlers[req.Cmd]
	sysMu.RUnlock()

	if isSys {
		log.Debug(ctx, "exec.sys.dispatch", "cmd", req.Cmd)
		err := handler(ctx, req.Args)
		dur := time.Since(start)
		res := &ExecResult{ExitCode: 0, Duration: dur}
		if err != nil {
			res.ExitCode = 1
			res.Stderr = err.Error()
			return res, &ExecError{Cmd: req.Cmd, Args: req.Args, ExitCode: 1, Stderr: err.Error()}
		}
		log.Info(ctx, "exec.ok", "cmd", req.Cmd, "duration_us", dur.Microseconds())
		return res, nil
	}

	// 2. Prepare Standard Execution
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
	isShell := false
	if (req.Cmd == "bash" || req.Cmd == "sh") && len(req.Args) >= 2 && req.Args[0] == "-c" {
		isShell = true
	}

	if isShell {
		waitErr = executeShell(ctx, log, req.Args[1], req.Cmd, req.Args[2:], req, stdin, stdout, stderr)
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
		errMsg := strings.TrimSpace(res.Stderr)
		log.Diagnose(ctx, "EXEC_FAILED", "command failed", "cmd", req.Cmd, "exit_code", exitCode, "duration_us", dur.Microseconds())
		return res, &ExecError{Cmd: req.Cmd, Args: req.Args, ExitCode: exitCode, Stderr: errMsg}
	}

	log.Info(ctx, "exec.ok", "cmd", req.Cmd, "duration_us", dur.Microseconds())
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
		return err
	}

	envList := append(os.Environ(), req.Env...)

	execHandler := func(ictx context.Context, args []string) error {
		start := time.Now()
		if len(args) > 0 {
			sysMu.RLock()
			handler, ok := sysHandlers[args[0]]
			sysMu.RUnlock()
			if ok {
				err := handler(ictx, args[1:])
				dur := time.Since(start)
				if err != nil {
					log.Error(ctx, "sh.sys.fail", "cmd", args[0], "error", err.Error())
				} else {
					log.Debug(ctx, "sh.sys.ok", "cmd", args[0], "duration_us", dur.Microseconds())
				}
				return err
			}
		}

		if len(args) > 0 {
			log.Debug(ctx, "sh.cmd.start", "cmd", args[0])
		}
		err := interp.DefaultExecHandler(0)(ictx, args)
		dur := time.Since(start)
		if len(args) > 0 {
			if err != nil {
				// Downgraded to Debug to prevent handled sub-command exits (like if grep -q ...) from polluting the trace
				log.Debug(ctx, "sh.cmd.fail", "cmd", args[0], "error", err.Error())
			} else {
				log.Debug(ctx, "sh.cmd.ok", "cmd", args[0], "duration_us", dur.Microseconds())
			}
		}
		return err
	}

	runner, _ := interp.New(
		interp.Env(expand.ListEnviron(envList...)),
		interp.Dir(req.Dir),
		interp.Params(scriptArgs...),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandler(execHandler),
	)
	return runner.Run(ctx, prog)
}

func executeBinary(ctx context.Context, log *Logx, req ExecRequest, stdin io.Reader, stdout, stderr io.Writer) (error, int) {
	cmd := exec.Command(req.Cmd, req.Args...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
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
