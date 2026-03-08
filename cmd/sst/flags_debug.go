package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	sst "github.com/swiss-software-tool/sst/internal/sst"
)

// AttachDebugFlags wires persistent top-level flags onto the root command,
// and applies them (as env + sst.ResetFromEnv) before any subcommand runs.
func AttachDebugFlags(root *cobra.Command) {
	var (
		flagDebug     bool   // = --level=debug + --trace
		flagLevel     string // debug|info|warn|error
		flagLevels    string // "probe=debug,hi=info"
		flagTrace     bool
		flagTZ        string // utc|local
		flagTimefmt   string // Go layout
		flagDepth     int    // N>0 => emit span_depth and drop logs deeper than N
		flagPropagate bool   // reserved (GitOps propagation)
		flagSession   string // reserved (correlation id)
	)

	// Persistent flags (visible on every subcommand)
	fs := root.PersistentFlags()
	fs.BoolVar(&flagDebug, "debug", false, "Shortcut for --level=debug --trace")
	fs.StringVar(&flagLevel, "level", "", "Global log level: debug|info|warn|error")
	fs.StringVar(&flagLevels, "levels", "", "Per-subsystem levels, e.g. \"probe=debug,hi=info\"")
	fs.BoolVar(&flagTrace, "trace", false, "Enable tracing (adds trace_id/span_id when spans are active)")
	fs.StringVar(&flagTZ, "tz", "", "Timestamp zone: utc|local")
	fs.StringVar(&flagTimefmt, "timefmt", "", "Go time layout (default: microsecond RFC3339 with numeric offset)")
	fs.IntVar(&flagDepth, "depth", 0, "Max span depth to log (N>0 keeps only top N and emits span_depth)")
	// reserved for GitOps propagation & correlation (ADR will wire these later)
	fs.BoolVar(&flagPropagate, "propagate-debug", false, "Propagate debug to affected services (GitOps) [reserved]")
	fs.StringVar(&flagSession, "debug-session", "", "Debug session id for correlation [reserved]")

	// Chain PersistentPreRunE so we don't clobber existing root hooks
	prev := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Composite behavior for --debug
		if flagDebug {
			if flagLevel == "" {
				flagLevel = "debug"
			}
			flagTrace = true
		}

		// Export envs that sst/logx understands (and children inherit)
		if flagLevel != "" {
			_ = os.Setenv("SST_LOG_LEVEL", strings.ToLower(flagLevel))
		}
		if flagLevels != "" {
			_ = os.Setenv("SST_LOG_LEVELS", flagLevels)
		}
		if flagTrace {
			_ = os.Setenv("SST_TRACING", "1")
		}
		if flagTZ != "" {
			_ = os.Setenv("SST_LOG_TZ", strings.ToLower(flagTZ))
		}
		if flagTimefmt != "" {
			_ = os.Setenv("SST_LOG_TIMEFMT", flagTimefmt)
		}
		if flagDepth > 0 {
			_ = os.Setenv("SST_LOG_DEPTH", strconv.Itoa(flagDepth))
		}

		// Apply to current process immediately
		sst.ResetFromEnv()

		// Call any previous root persistent hook
		if prev != nil {
			return prev(cmd, args)
		}
		return nil
	}
}
