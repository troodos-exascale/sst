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
	"github.com/troodos-exascale/sst/internal/sst"
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
	submodules []string

	// chunking state
	currentBuilder strings.Builder
	fileIndex      int
	maxChunkSize   int

	// behavior
	methods summariseMethods

	// framework execution state
	steps *sst.StepsRunState
}

// flush writes the current buffered data to disk with an incremented suffix
func (app *summariseApp) flush() error {
	if app.currentBuilder.Len() == 0 {
		return nil
	}

	data := app.currentBuilder.String()
	app.currentBuilder.Reset()
	app.fileIndex++

	out := app.outputPath
	if out == "-" {
		fmt.Print(data)
		return nil
	}

	if !filepath.IsAbs(out) {
		out = filepath.Join(app.repoRoot, out)
	}

	// Insert chunk suffix (_1, _2, etc.) before the extension
	ext := filepath.Ext(out)
	base := strings.TrimSuffix(out, ext)
	finalOut := fmt.Sprintf("%s_%d%s", base, app.fileIndex, ext)

	if err := os.MkdirAll(filepath.Dir(finalOut), 0o755); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[summarise] Writing chunk %d (%d bytes) to %s\n", app.fileIndex, len(data), finalOut)
	return os.WriteFile(finalOut, []byte(data), 0o644)
}

// appendBlock ensures that adding a new file string block won't exceed the max size.
// If it does, we flush the current buffer to a new file first.
func (app *summariseApp) appendBlock(block string) error {
	if app.currentBuilder.Len() > 0 && app.currentBuilder.Len()+len(block) > app.maxChunkSize {
		if err := app.flush(); err != nil {
			return err
		}
	}
	app.currentBuilder.WriteString(block)
	return nil
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
	AppendLsTree func(*summariseApp) error
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
	chunkSizeKB  int
)

var summariseCmd = &cobra.Command{
	Use:   "summarise [submodules...]",
	Short: "Consolidate repository files into chunked reports.",
	RunE: func(cmd *cobra.Command, args []string) error {

		app := &summariseApp{
			submodules:   args,
			outputPath:   outputFile,
			maxChunkSize: chunkSizeKB * 1024,
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
	summariseCmd.Flags().IntVar(&chunkSizeKB, "chunk-size", 300, "Max size of each chunk file in KB")
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

	header := fmt.Sprintf("# Repository Summary: %s\n* **Date:** %s\n\n", app.repoRoot, time.Now().UTC().Format(time.RFC1123))
	return app.appendBlock(header)
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
			errBlock := fmt.Sprintf("FILE: %s\n--- ERROR: %v ---\nEND: %s\n\n", f, err, f)
			if appendErr := app.appendBlock(errBlock); appendErr != nil {
				return appendErr
			}
			continue
		}

		// Quick heuristic to skip binaries: check for null bytes
		if bytes.Contains(data, []byte{0}) {
			skipBlock := fmt.Sprintf("FILE: %s\n--- SKIPPED BINARY FILE ---\nEND: %s\n\n", f, f)
			if appendErr := app.appendBlock(skipBlock); appendErr != nil {
				return appendErr
			}
			continue
		}

		var block strings.Builder
		fmt.Fprintf(&block, "FILE: %s\n", f)
		block.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			block.WriteByte('\n')
		}
		fmt.Fprintf(&block, "END: %s\n\n", f)

		if appendErr := app.appendBlock(block.String()); appendErr != nil {
			return appendErr
		}
	}
	return nil
}

func summariseAppendLsTree(app *summariseApp) error {
	var block strings.Builder
	fmt.Fprintf(&block, "\n\n---\n\n# Repository tree (ls -R)\n\n")

	cmd := exec.Command("ls", "-R")
	cmd.Dir = app.repoRoot
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	block.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		block.WriteByte('\n')
	}
	
	return app.appendBlock(block.String())
}

func summariseWriteOutput(app *summariseApp) error {
	return app.flush()
}
