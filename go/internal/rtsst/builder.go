package rtsst

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// Instruction represents a parsed line or block from the Dockerfile
type Instruction struct {
	Type string
	Args string
}

// BuildApp is the superblock for the mkimage build process
type BuildApp struct {
	Log          *sst.Logx
	Files        sst.Files
	Dockerfile   string
	BaseImage    string
	Name         string
	Emits        []string
	Instructions []Instruction

	BuildArgs map[string]string // Supplied by CLI
	Args      map[string]string // Tracked during execution
	Env       map[string]string // Tracked during execution

	// Paths for reproducible staging
	RuntimeDir  string
	RootDir     string
	OCIStaging  string
	ImageOutput string
}

func NewBuilder(log *sst.Logx, files sst.Files, dockerfile, runtimeDir string) *BuildApp {
	return &BuildApp{
		Log:        log.Subsystem("rtsst-builder"),
		Files:      files,
		Dockerfile: dockerfile,
		RuntimeDir: runtimeDir,
		BuildArgs:  make(map[string]string),
		Args:       make(map[string]string),
		Env:        make(map[string]string),
	}
}

// Parse implements the line-by-line scanner for the YAML-hybrid Dockerfile
func (b *BuildApp) Parse(ctx context.Context) error {
	f, err := os.Open(b.Dockerfile)
	if err != nil {
		return fmt.Errorf("failed to open Dockerfile: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var currentBlock []string
	var currentType string
	isMultiline := false
	var continuedLine string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if isMultiline {
			if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				b.Instructions = append(b.Instructions, Instruction{
					Type: currentType,
					Args: strings.Join(currentBlock, "\n"),
				})
				currentBlock = nil
				isMultiline = false
			} else {
				blockLine := strings.TrimPrefix(line, "    ")
				blockLine = strings.TrimPrefix(blockLine, "\t")
				currentBlock = append(currentBlock, blockLine)
				continue
			}
		}

		// Handle standard backslash continuations for non-multiline commands
		if !isMultiline && strings.HasSuffix(trimmed, "\\") {
			continuedLine += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		if continuedLine != "" {
			trimmed = strings.TrimSpace(continuedLine + trimmed)
			continuedLine = ""
		}

		if strings.HasSuffix(trimmed, ": |") {
			currentType = strings.TrimSuffix(trimmed, ": |")
			isMultiline = true
			continue
		}

		parts := strings.SplitN(trimmed, " ", 2)
		if len(parts) == 2 {
			instType := strings.ToUpper(parts[0])
			instType = strings.TrimSuffix(instType, ":") // support NAME:, EMIT:, etc.
			b.Instructions = append(b.Instructions, Instruction{
				Type: instType,
				Args: strings.TrimSpace(parts[1]),
			})
		}
	}

	if isMultiline && len(currentBlock) > 0 {
		b.Instructions = append(b.Instructions, Instruction{
			Type: currentType,
			Args: strings.Join(currentBlock, "\n"),
		})
	}

	return scanner.Err()
}

// PreProcess evaluates variables required to resolve FROM, NAME, and EMIT before execution.
func (b *BuildApp) PreProcess(ctx context.Context) error {
	tmpArgs := make(map[string]string)
	for k, v := range b.BuildArgs {
		tmpArgs[k] = v
	}
	tmpEnv := make(map[string]string)

	for _, inst := range b.Instructions {
		args := os.Expand(inst.Args, func(k string) string {
			if v, ok := tmpEnv[k]; ok { return v }
			if v, ok := tmpArgs[k]; ok { return v }
			return ""
		})

		// Safely strip inline comments for variable evaluations
		cleanArgs := strings.SplitN(args, " #", 2)[0]
		cleanArgs = strings.TrimSpace(cleanArgs)

		switch inst.Type {
		case "ARG":
			parts := strings.SplitN(cleanArgs, "=", 2)
			if len(parts) == 2 {
				tmpArgs[parts[0]] = parts[1]
			}
		case "ENV":
			parts := strings.SplitN(cleanArgs, "=", 2)
			if len(parts) == 2 {
				tmpEnv[parts[0]] = parts[1]
			} else {
				parts = strings.SplitN(cleanArgs, " ", 2)
				if len(parts) == 2 {
					tmpEnv[parts[0]] = parts[1]
				}
			}
		case "FROM":
			if b.BaseImage == "" {
				b.BaseImage = cleanArgs
			}
			// Standard Docker behavior: ARGs are cleared after FROM
			tmpArgs = make(map[string]string)
			for k, v := range b.BuildArgs {
				tmpArgs[k] = v
			}
		case "NAME", "VMOUT": // Support legacy VMOUT gracefully
			b.Name = cleanArgs
		case "EMIT":
			emits := strings.Split(cleanArgs, ",")
			for _, e := range emits {
				b.Emits = append(b.Emits, strings.ToLower(strings.TrimSpace(e)))
			}
		}
	}

	if b.Name == "" {
		base := filepath.Base(b.Dockerfile)
		b.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	// Default to generating a raw image if nothing specified
	if len(b.Emits) == 0 {
		b.Emits = append(b.Emits, "raw")
	}

	if b.BaseImage == "" {
		return fmt.Errorf("no FROM instruction found in Dockerfile")
	}

	return nil
}

// Substitute expands environment and build variables into a string
func (b *BuildApp) Substitute(val string) string {
	return os.Expand(val, func(k string) string {
		if v, ok := b.Env[k]; ok { return v }
		if v, ok := b.Args[k]; ok { return v }
		return ""
	})
}
