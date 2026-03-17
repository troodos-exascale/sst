package sst

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Levels & Colors
// ============================================================================

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
	colorBlue   = "\033[36m" // Cyan-ish for better visibility
	colorGray   = "\033[90m"
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
	envLevel := os.Getenv("SST_LOG_LEVEL")
	if envLevel == "" {
		envLevel = "info"
	}

	if !isLevelEnabled(Level(envLevel), level) {
		return
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

	// Output Routing:
	// Check SST_LOG_FORMAT environment variable first.
	format := strings.ToLower(os.Getenv("SST_LOG_FORMAT"))
	usePretty := false
	if format == "text" {
		usePretty = true
	} else if format == "json" {
		usePretty = false
	} else {
		// Fallback to original TTY detection
		usePretty = l.isTTY && l.outTTY != nil
	}

	if usePretty {
		l.emitPretty(level, msg, finalFields)
	} else if l.outJSON != nil {
		b, _ := json.Marshal(ev)
		// Append \r\n explicitly to protect against raw mode pipes
		fmt.Fprintf(l.outJSON, "%s\r\n", string(b))
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

	// Select the correct writer in case outTTY is nil (e.g. forced via format string)
	out := l.outTTY
	if out == nil {
		out = l.outJSON
	}

	if out != nil {
		// Explicit CR + LF ensures no staircasing in raw terminal mode
		fmt.Fprintf(out, "%s\r\n", line)
	}
}

func formatPretty(sub string, level Level, msg string, f map[string]interface{}) string {
	var sb strings.Builder

	// 1. Time
	sb.WriteString(colorGray)
	sb.WriteString(time.Now().Format("15:04:05.000 "))
	sb.WriteString(colorReset)

	// 2. Level
	lvlStr := fmt.Sprintf("%-5s", strings.ToUpper(string(level)))
	switch level {
	case LevelError:
		sb.WriteString(colorRed + lvlStr + colorReset)
	case LevelWarn:
		sb.WriteString(colorYellow + lvlStr + colorReset)
	case LevelInfo:
		sb.WriteString(colorBlue + lvlStr + colorReset)
	case LevelDebug:
		sb.WriteString(colorGray + lvlStr + colorReset)
	default:
		sb.WriteString(lvlStr)
	}
	sb.WriteString(" ")

	// 3. Subsystem
	if sub != "" {
		sb.WriteString(colorGray + "[" + sub + "] " + colorReset)
	}

	// Extract standard error strings so we can indent them at the bottom
	var stderrStr string
	if s, ok := f["stderr"].(string); ok && s != "" {
		stderrStr = s
		delete(f, "stderr")
	}

	// 4. Message (with special overrides handling \r\n safely)
	switch msg {
	case "image.convert":
		sb.WriteString(fmt.Sprintf("converting image → raw\r\n  %v → %v", f["from"], f["to"]))
		delete(f, "from")
		delete(f, "to")
	case "exec.start":
		sb.WriteString(fmt.Sprintf("$ %v", f["cmdline"]))
		// Nip the double-output in the bud!
		delete(f, "cmdline")
		delete(f, "args")
		delete(f, "cmd")
		delete(f, "mode")
	case "exec.ok":
		if d, ok := f["duration_ms"]; ok {
			sb.WriteString(fmt.Sprintf("✓ done (%v ms)", d))
			delete(f, "duration_ms")
		} else {
			sb.WriteString("✓ done")
		}
		// Stop dumping the script again on success!
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

	// 5. Append remaining structured context (sorted for stability)
	if len(f) > 0 {
		var keys []string
		for k := range f {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		sb.WriteString(colorGray + " |")
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf(" %s=%v", k, f[k]))
		}
		sb.WriteString(colorReset)
	}

	// 6. Append multi-line errors cleanly
	if stderrStr != "" {
		sb.WriteString("\r\n" + colorRed + indent(stderrStr) + colorReset)
	}

	return sb.String()
}

// indent ensures multi-line strings maintain carriage returns
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		// Strip existing CRs to prevent doubling, then format
		lines[i] = "  " + strings.TrimRight(lines[i], "\r")
	}
	// Re-join with strict CR+LF
	return strings.Join(lines, "\r\n")
}

func ResetFromEnv() {}

func StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
