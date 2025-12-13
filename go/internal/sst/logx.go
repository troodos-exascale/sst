package sst

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// zl is the unexported implementation of the Logx interface.
type zl struct {
	base      zerolog.Logger
	subsystem string
}

// newLogx creates a new logger instance writing to stdout. It is unexported.
func newLogx() Logx {
	return NewWithWriter(os.Stdout)
}

// NewWithWriter is a helper for creating a logger with a specific writer, useful for tests.
func NewWithWriter(w io.Writer) Logx {
	zlog := zerolog.New(w).With().Timestamp().Logger()
	return &zl{base: zlog}
}

func (l *zl) Subsystem(name string) Logx {
	ll := l.base.With().Str("subsystem", name).Logger()
	return &zl{base: ll, subsystem: name}
}

func (l *zl) With(kv ...any) Logx {
	b := l.base.With()
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		b = b.Interface(k, kv[i+1])
	}
	return &zl{base: b.Logger(), subsystem: l.subsystem}
}

func (l *zl) WithError(err error) Logx {
	if err == nil {
		return l
	}
	return &zl{base: l.base.With().Err(err).Logger(), subsystem: l.subsystem}
}

func (l *zl) log(ctx context.Context, want Level, msg string, kv ...any) {
	if lim := depthLimit(); lim > 0 {
		if d := getDepth(ctx); d > lim {
			return
		}
	}
	if !shouldLog(l.subsystem, want) {
		return
	}
	ev := l.base.With()
	if tid, sid := TraceIDs(ctx); tid != "" {
		ev = ev.Str("trace_id", tid)
		if sid != "" {
			ev = ev.Str("span_id", sid)
		}
	}
	if lim := depthLimit(); lim > 0 {
		ev = ev.Int("span_depth", getDepth(ctx))
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			ev = ev.Interface(k, kv[i+1])
		}
	}
	ll := ev.Logger()
	switch want {
	case LevelDebug:
		ll.Debug().Msg(msg)
	case LevelWarn:
		ll.Warn().Msg(msg)
	case LevelError:
		ll.Error().Msg(msg)
	default:
		ll.Info().Msg(msg)
	}
}

func (l *zl) Debug(ctx context.Context, msg string, kv ...any) { l.log(ctx, LevelDebug, msg, kv...) }
func (l *zl) Info(ctx context.Context, msg string, kv ...any)  { l.log(ctx, LevelInfo, msg, kv...) }
func (l *zl) Warn(ctx context.Context, msg string, kv ...any)  { l.log(ctx, LevelWarn, msg, kv...) }
func (l *zl) Error(ctx context.Context, msg string, kv ...any) { l.log(ctx, LevelError, msg, kv...) }

// --- Package-level log configuration ---

type Level int32

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	globalLevel     atomic.Int32
	subsystemLevels atomic.Value // map[string]Level
)

func init() {
	layout := os.Getenv("SST_LOG_TIMEFMT")
	if layout == "" {
		layout = "2006-01-02T15:04:05.000000-07:00"
	}
	zerolog.TimeFieldFormat = layout
	if strings.EqualFold(os.Getenv("SST_LOG_TZ"), "utc") {
		zerolog.TimestampFunc = func() time.Time { return time.Now().UTC() }
	} else {
		zerolog.TimestampFunc = func() time.Time { return time.Now() }
	}
	ResetFromEnv()
}

func ResetFromEnv() {
	globalLevel.Store(int32(parseLevel(os.Getenv("SST_LOG_LEVEL"))))
	if v := os.Getenv("SST_LOG_LEVELS"); v != "" {
		subsystemLevels.Store(parseSubsystemLevels(v))
	} else {
		subsystemLevels.Store(map[string]Level{})
	}
	reloadDepthLimitFromEnv()
}

func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug": return LevelDebug
	case "warn": return LevelWarn
	case "error": return LevelError
	default: return LevelInfo
	}
}

func parseSubsystemLevels(s string) map[string]Level {
	out := map[string]Level{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" || !strings.Contains(p, "=") {
			continue
		}
		parts := strings.SplitN(p, "=", 2)
		out[parts[0]] = parseLevel(parts[1])
	}
	return out
}

func shouldLog(subsystem string, want Level) bool {
	if m, _ := subsystemLevels.Load().(map[string]Level); m != nil {
		if lv, ok := m[subsystem]; ok {
			return want >= lv
		}
	}
	return want >= Level(globalLevel.Load())
}

func SafeExit(ctx context.Context, l Logx, exitCode int) {
	if l != nil {
		l.Info(ctx, "exiting", "exit_code", exitCode)
	}
	time.Sleep(10 * time.Millisecond)
}

// --- Tracing and gRPC ---

var tracer trace.Tracer

type depthKey struct{}
var depthLimitAtomic atomic.Int64

func depthLimit() int { return int(depthLimitAtomic.Load()) }

func reloadDepthLimitFromEnv() {
	v := os.Getenv("SST_LOG_DEPTH")
	if v == "" {
		depthLimitAtomic.Store(0)
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		depthLimitAtomic.Store(0)
		return
	}
	depthLimitAtomic.Store(int64(n))
}

func getDepth(ctx context.Context) int {
	if v := ctx.Value(depthKey{}); v != nil {
		if d, ok := v.(int); ok { return d }
	}
	return 0
}

func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

type ids struct{ traceID, spanID string }
type idsKey struct{}

func withIDs(ctx context.Context, tid, sid string) context.Context {
	return context.WithValue(ctx, idsKey{}, ids{tid, sid})
}

func getIDs(ctx context.Context) (string, string) {
	if v := ctx.Value(idsKey{}); v != nil {
		if s, ok := v.(ids); ok { return s.traceID, s.spanID }
	}
	return "", ""
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TracingEnabled() bool { return os.Getenv("SST_TRACING") == "1" }

func StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	parentDepth := getDepth(ctx)
	childDepth := parentDepth + 1
	if !TracingEnabled() {
		return withDepth(ctx, childDepth), func(error) {}
	}
	ctx2, sp := tracer.Start(ctx, name)
	ctx2 = withDepth(ctx2, childDepth)
	if sc := trace.SpanContextFromContext(ctx2); sc.IsValid() {
		return withIDs(ctx2, sc.TraceID().String(), sc.SpanID().String()), func(error) { sp.End() }
	}
	return withIDs(ctx2, randHex(16), randHex(8)), func(error) { sp.End() }
}

func TraceIDs(ctx context.Context) (traceID, spanID string) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String(), sc.SpanID().String()
	}
	return getIDs(ctx)
}

func GRPCClientUnaryInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if TracingEnabled() {
			var end func(error)
			ctx, end = StartSpan(ctx, "grpc:"+method)
			defer end(nil)
			carrier := metadata.New(nil)
			otel.GetTextMapPropagator().Inject(ctx, metadataCarrier{carrier})
			md, _ := metadata.FromOutgoingContext(ctx)
			ctx = metadata.NewOutgoingContext(ctx, metadata.Join(md, carrier))
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

type metadataCarrier struct{ md metadata.MD }
func (c metadataCarrier) Get(key string) string {
	vals := c.md.Get(key)
	if len(vals) > 0 { return vals[0] }
	return ""
}
func (c metadataCarrier) Set(key, value string) { c.md.Set(key, value) }
func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for k := range c.md { keys = append(keys, k) }
	return keys
}
