package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/sst"
)

//
// =========================
// Application root (superblock)
// =========================

type summariseApp struct {
	// dynamic state
	repoRoot   string
	files      []string
	outputPath string
	builder    strings.Builder
	submodules []string

	// behavior
	methods summariseMethods

	// framework execution state
	steps *sst.StepsRunState
}

//
// =========================
// Application methods table
// =========================

type summariseMethods struct {
	ApplyFlags   func(*summariseApp) error
	FindRepo     func(*summariseApp) error
	ListFiles    func(*summariseApp) error
	WriteResult  func(*summariseApp) error
	AppendLsTree func(*summariseApp) error // NEW
	WriteOutput  func(*summariseApp) error
}

var summariseOps = summariseMethods{
	ApplyFlags:   summariseApplyFlags,
	FindRepo:     summariseFindRepo,
	ListFiles:    summariseListFiles,
	WriteResult:  summariseWriteResult,
	AppendLsTree: summariseAppendLsTree,
	WriteOutput:  summariseWriteOutput,
}

//
// =========================
// CLI wiring
// =========================

var (
	repoRootFlag string
	outputFile   string
)

var summariseCmd = &cobra.Command{
	Use:   "summarise [submodules...]",
	Short: "Consolidate repository files into a single report.",
	RunE: func(cmd *cobra.Command, args []string) error {

		app := &summariseApp{
			submodules: args,
			outputPath: outputFile,
		}

		app.methods = summariseOps

		app.steps = sst.StepsInit("summarise")
		app.steps.StepsWithLogger(func(f string, a ...any) {
			fmt.Fprintf(os.Stderr, "[summarise] "+f+"\n", a...)
		})

		// bind steps to methods
		app.steps.StepsAdd("apply flags", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ApplyFlags(app)
		})
		app.steps.StepsAdd("find repo", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.FindRepo(app)
		})
		app.steps.StepsAdd("list files", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ListFiles(app)
		})
		app.steps.StepsAdd("write result", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.WriteResult(app)
		})
		app.steps.StepsAdd("append ls -R", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.AppendLsTree(app)
		})
		app.steps.StepsAdd("write output", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.WriteOutput(app)
		})

		return sst.StepsRun(app.steps)
	},
}

func init() {
	rootCmd.AddCommand(summariseCmd)
	summariseCmd.Flags().StringVarP(&repoRootFlag, "repo-root", "r", "", "Repo root")
	summariseCmd.Flags().StringVarP(&outputFile, "output", "o", "docs/repo-summary.md", "Output file")
}

//
// =========================
// Application method implementations (private)
// =========================

func summariseApplyFlags(app *summariseApp) error {
	if repoRootFlag != "" {
		app.repoRoot = repoRootFlag
	}
	return nil
}

func summariseFindRepo(app *summariseApp) error {
	if app.repoRoot != "" {
		return nil
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return err
	}
	app.repoRoot = strings.TrimSpace(string(out))

	fmt.Fprintf(&app.builder, "# Repository Summary: %s\n", app.repoRoot)
	fmt.Fprintf(&app.builder, "* **Date:** %s\n\n", time.Now().UTC().Format(time.RFC1123))
	return nil
}

func summariseListFiles(app *summariseApp) error {
	// -c: cached (tracked files)
	// -o: others (untracked files)
	// --exclude-standard: respect .gitignore
	cmd := exec.Command("git", "ls-files", "-z", "-c", "-o", "--exclude-standard")
	cmd.Dir = app.repoRoot
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	files := strings.Split(string(out), "\x00")
	for _, f := range files {
		if f != "" {
			app.files = append(app.files, f)
		}
	}
	sort.Strings(app.files)
	return nil
}

func summariseWriteResult(app *summariseApp) error {
	for _, f := range app.files {
		data, err := os.ReadFile(filepath.Join(app.repoRoot, f))
		if err != nil {
			fmt.Fprintf(&app.builder, "FILE: %s\n--- ERROR: %v ---\nEND: %s\n\n", f, err, f)
			continue
		}

		// Quick heuristic to skip binaries: check for null bytes
		if bytes.Contains(data, []byte{0}) {
			fmt.Fprintf(&app.builder, "FILE: %s\n--- SKIPPED BINARY FILE ---\nEND: %s\n\n", f, f)
			continue
		}

		fmt.Fprintf(&app.builder, "FILE: %s\n", f)
		app.builder.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			app.builder.WriteByte('\n')
		}
		fmt.Fprintf(&app.builder, "END: %s\n\n", f)
	}
	return nil
}

func summariseAppendLsTree(app *summariseApp) error {
	fmt.Fprintf(&app.builder, "\n\n---\n\n# Repository tree (ls -R)\n\n")

	cmd := exec.Command("ls", "-R")
	cmd.Dir = app.repoRoot
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	app.builder.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		app.builder.WriteByte('\n')
	}
	return nil
}

func summariseWriteOutput(app *summariseApp) error {
	out := app.outputPath
	if out == "-" {
		fmt.Print(app.builder.String())
		return nil
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(app.repoRoot, out)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, []byte(app.builder.String()), 0o644)
}
