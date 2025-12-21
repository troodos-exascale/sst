package sst

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
    "strings"
	"golang.org/x/term"
)

// ============================================================================
// Levels
// ============================================================================

type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// ============================================================================
// Logx
// ============================================================================

type Logx struct {
	subsystem string
	fields    map[string]interface{}

	outJSON io.Writer
	outTTY  io.Writer
	isTTY   bool

	mu sync.Mutex

	lastPretty string
}

// ============================================================================
// Constructors
// ============================================================================

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

// NewWithWriter creates a logger that writes JSON to the provided writer.
// Useful for testing.
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

// ============================================================================
// Context enrichment
// ============================================================================

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

// ============================================================================
// Public API
// ============================================================================

func (l *Logx) Debug(ctx interface{}, msg string, kv ...interface{}) {
	l.emit(LevelDebug, msg, kv...)
}

func (l *Logx) Info(ctx interface{}, msg string, kv ...interface{}) {
	l.emit(LevelInfo, msg, kv...)
}

func (l *Logx) Warn(ctx interface{}, msg string, kv ...interface{}) {
	l.emit(LevelWarn, msg, kv...)
}

func (l *Logx) Error(ctx interface{}, msg string, kv ...interface{}) {
	l.emit(LevelError, msg, kv...)
}

func (l *Logx) Diagnose(ctx interface{}, code string, msg string, kv ...interface{}) {
	kv = append(kv,
		"diagnostic", true,
		"retain", true,
		"code", code,
	)
	l.emit(LevelError, msg, kv...)
}

func SafeExit(ctx context.Context, log *Logx, code int) {
	os.Exit(code)
}

// ============================================================================
// Emit
// ============================================================================

func (l *Logx) emit(level Level, msg string, kv ...interface{}) {
	// Simple environment check for log level
	envLevel := os.Getenv("SST_LOG_LEVEL")
	// FIX: Default to Info if not set
	if envLevel == "" {
		envLevel = "info"
	}

	if !isLevelEnabled(Level(envLevel), level) {
		return
	}

	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Merge fields temporarily for this emit
	finalFields := make(map[string]interface{}, len(l.fields)+len(kv)/2)
	for k, v := range l.fields {
		finalFields[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			finalFields[k] = kv[i+1]
		}
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

	// JSON output (machine)
	if l.outJSON != nil {
		_ = json.NewEncoder(l.outJSON).Encode(ev)
	}

	// Pretty output (human)
	if l.isTTY && l.outTTY != nil {
		l.emitPretty(level, msg, finalFields)
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

// ============================================================================
// Pretty output (TTY)
// ============================================================================

func (l *Logx) emitPretty(level Level, msg string, fields map[string]interface{}) {
	line := formatPretty(l.subsystem, level, msg, fields)
	if line == l.lastPretty {
		return
	}
	l.lastPretty = line
	fmt.Fprintln(l.outTTY, line)
}

func formatPretty(sub string, level Level, msg string, f map[string]interface{}) string {
	prefix := ""
	if sub != "" {
		prefix = "[" + sub + "] "
	}

	switch msg {
	case "image.convert":
		return fmt.Sprintf("%sconverting image → raw\n  %v → %v",
			prefix, f["from"], f["to"])
	case "exec.start":
		return fmt.Sprintf("%s$ %v", prefix, f["cmdline"])
	case "exec.ok":
		if d, ok := f["duration_ms"]; ok {
			return fmt.Sprintf("%s✓ done (%v ms)", prefix, d)
		}
		return prefix + "✓ done"
	}

	if level == LevelError {
		if s, ok := f["stderr"].(string); ok && s != "" {
			return fmt.Sprintf("%s✗ %s\n%s",
				prefix, msg, indent(s))
		}
		return fmt.Sprintf("%s✗ %s", prefix, msg)
	}

	return prefix + msg
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func ResetFromEnv() {}

func StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
