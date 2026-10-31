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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[36m"
	colorGray   = "\033[37m"
)

type contextKey string

const (
	spanDepthKey contextKey = "sst.span_depth"
)

type Logx struct {
	subsystem string
	fields    map[string]interface{}
	outJSON   io.Writer
	outTTY    io.Writer
	isTTY     bool
	mu        sync.Mutex
	lastPretty string
}

func LogxRoot() *Logx {
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	l := &Logx{
		fields:  map[string]interface{}{},
		outJSON: os.Stdout,
		isTTY:   isTTY,
	}
	if isTTY {
		l.outTTY = os.Stdout
	}
	return l
}

func NewWithWriter(w io.Writer) *Logx {
	return &Logx{
		fields:  map[string]interface{}{},
		outJSON: w,
		isTTY:   false,
	}
}

func (l *Logx) Subsystem(name string) *Logx {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &Logx{
		subsystem: name,
		fields:    l.copyFields(),
		outJSON:   l.outJSON,
		outTTY:    l.outTTY,
		isTTY:     l.isTTY,
	}
}

func (l *Logx) copyFields() map[string]interface{} {
	dst := make(map[string]interface{}, len(l.fields))
	for k, v := range l.fields {
		dst[k] = v
	}
	return dst
}

func (l *Logx) With(kv ...interface{}) *Logx {
	l.mu.Lock()
	defer l.mu.Unlock()
	newFields := l.copyFields()
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			newFields[k] = kv[i+1]
		}
	}
	return &Logx{
		subsystem: l.subsystem,
		fields:    newFields,
		outJSON:   l.outJSON,
		outTTY:    l.outTTY,
		isTTY:     l.isTTY,
	}
}

func (l *Logx) WithError(err error) *Logx {
	if err != nil {
		return l.With("error", err.Error())
	}
	return l
}

func (l *Logx) Debug(ctx context.Context, msg string, kv ...interface{}) {
	l.emit(ctx, LevelDebug, msg, kv...)
}

func (l *Logx) Info(ctx context.Context, msg string, kv ...interface{}) {
	l.emit(ctx, LevelInfo, msg, kv...)
}

func (l *Logx) Warn(ctx context.Context, msg string, kv ...interface{}) {
	l.emit(ctx, LevelWarn, msg, kv...)
}

func (l *Logx) Error(ctx context.Context, msg string, kv ...interface{}) {
	l.emit(ctx, LevelError, msg, kv...)
}

func (l *Logx) Diagnose(ctx context.Context, code string, msg string, kv ...interface{}) {
	kv = append(kv, "diagnostic", true, "retain", true, "code", code)
	l.emit(ctx, LevelError, msg, kv...)
}

func SafeExit(ctx context.Context, log *Logx, code int) {
	os.Exit(code)
}

func (l *Logx) emit(ctx context.Context, level Level, msg string, kv ...interface{}) {
	envLevel := os.Getenv("SST_LOG_LEVEL")
	if envLevel == "" {
		envLevel = "info"
	}
	if !isLevelEnabled(Level(envLevel), level) {
		return
	}

	// Depth Filtering
	currentDepth := 0
	if ctx != nil {
		if d, ok := ctx.Value(spanDepthKey).(int); ok {
			currentDepth = d
		}
	}
	
	if depthLimitStr := os.Getenv("SST_LOG_DEPTH"); depthLimitStr != "" {
		if limit, err := strconv.Atoi(depthLimitStr); err == nil && limit > 0 {
			if currentDepth > limit {
				return
			}
		}
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	finalFields := make(map[string]interface{}, len(l.fields)+len(kv)/2)
	for k, v := range l.fields {
		finalFields[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			finalFields[k] = kv[i+1]
		}
	}

	if currentDepth > 0 {
		finalFields["span_depth"] = currentDepth
	}

	ev := map[string]interface{}{
		"level":   level,
		"message": msg,
		"time":    now.Format(time.RFC3339Nano),
	}
	if l.subsystem != "" {
		ev["subsystem"] = l.subsystem
	}
	for k, v := range finalFields {
		ev[k] = v
	}

	format := strings.ToLower(os.Getenv("SST_LOG_FORMAT"))
	usePretty := false
	if format == "text" {
		usePretty = true
	} else if format == "json" {
		usePretty = false
	} else {
		usePretty = l.isTTY && l.outTTY != nil
	}

	if usePretty {
		l.emitPretty(level, msg, finalFields)
	} else if l.outJSON != nil {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(l.outJSON, "%s\n", string(b))
	}
}

func isLevelEnabled(configLevel, msgLevel Level) bool {
	levels := map[Level]int{
		LevelDebug: 0,
		LevelInfo:  1,
		LevelWarn:  2,
		LevelError: 3,
	}
	return levels[msgLevel] >= levels[configLevel]
}

func (l *Logx) emitPretty(level Level, msg string, fields map[string]interface{}) {
	line := formatPretty(l.subsystem, level, msg, fields)
	if line == l.lastPretty {
		return
	}
	l.lastPretty = line

	out := l.outTTY
	if out == nil {
		out = l.outJSON
	}
	if out != nil {
		fmt.Fprintf(out, "%s\n", line)
	}
}

func formatPretty(sub string, level Level, msg string, f map[string]interface{}) string {
	var sb strings.Builder
	sb.WriteString(colorGray)
	sb.WriteString(time.Now().Format("15:04:05.000 "))
	sb.WriteString(colorReset)

	if level != LevelInfo {
		lvlStr := fmt.Sprintf("%-5s", strings.ToUpper(string(level)))
		switch level {
		case LevelError:
			sb.WriteString(colorRed + lvlStr + colorReset)
		case LevelWarn:
			sb.WriteString(colorYellow + lvlStr + colorReset)
		case LevelDebug:
			sb.WriteString(colorGray + lvlStr + colorReset)
		}
		sb.WriteString(" ")
	}

	if sub != "" {
		sb.WriteString(colorGray + "[" + sub + "] " + colorReset)
	}

	var stderrStr string
	if s, ok := f["stderr"].(string); ok && s != "" {
		stderrStr = s
		delete(f, "stderr")
	}

	switch msg {
	case "exec.start":
		cmdline := fmt.Sprintf("%v", f["cmdline"])
		if strings.HasPrefix(cmdline, "bash -c ") {
			cmdline = strings.TrimPrefix(cmdline, "bash -c ")
		}
		cmdline = strings.Trim(cmdline, `"'`)
		sb.WriteString(fmt.Sprintf("\033[32m$ %v\033[0m", cmdline))
		delete(f, "cmdline")
		delete(f, "args")
		delete(f, "cmd")
		delete(f, "mode")
	case "exec.ok":
		if d, ok := f["duration_us"]; ok {
			sb.WriteString(fmt.Sprintf("✓ done (%v µs)", d))
			delete(f, "duration_us")
		} else {
			sb.WriteString("✓ done")
		}
		delete(f, "cmdline")
		delete(f, "cmd")
		delete(f, "args")
	default:
		if level == LevelError {
			sb.WriteString(colorRed + "✗ " + msg + colorReset)
		} else {
			sb.WriteString(msg)
		}
	}

	if len(f) > 0 {
		var keys []string
		for k := range f {
			if k == "span_depth" { continue }
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			sb.WriteString(colorGray + " |")
			for _, k := range keys {
				sb.WriteString(fmt.Sprintf(" %s=%v", k, f[k]))
			}
			sb.WriteString(colorReset)
		}
	}

	if stderrStr != "" {
		sb.WriteString("\n" + colorRed + indent(stderrStr) + colorReset)
	}

	return sb.String()
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func ResetFromEnv() {
	// In this implementation, emit() reads env directly for dynamic testing.
}

func StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	depth := 0
	if ctx != nil {
		if d, ok := ctx.Value(spanDepthKey).(int); ok {
			depth = d
		}
	}
	newCtx := context.WithValue(ctx, spanDepthKey, depth+1)
	return newCtx, func(err error) {}
}
