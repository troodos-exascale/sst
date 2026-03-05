package rtsst

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// Instruction represents a parsed line or block in the IR
type Instruction struct {
	Type       string
	Args       string
	Location   string   // "root", "base", "boot"
	Context    string   // "chroot", "host"
	Conditions []string // "all", "oci", "qcow2", "aws", etc.
}

type BuildApp struct {
	Log          *sst.Logx
	Files        sst.Files
	Dockerfile   string
	BaseImage    string
	Name         string
	Emits        []string
	Instructions []Instruction

	BuildArgs map[string]string
	Args      map[string]string
	Env       map[string]string

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

func parseModifiers(modStr string) (string, string, []string) {
	loc := "root"
	ctx := "chroot"
	conds := []string{"all"}

	if modStr == "" {
		return loc, ctx, conds
	}

	mods := strings.Split(modStr, ",")
	var parsedConds []string
	for _, m := range mods {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		switch m {
		case "root", "base", "boot":
			loc = m
		case "chroot", "host":
			ctx = m
		default:
			parsedConds = append(parsedConds, m)
		}
	}
	
	if len(parsedConds) > 0 {
		conds = parsedConds
	}
	return loc, ctx, conds
}

func desugarCopy(args string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "COPY", Args: args} 
	}
	dst := parts[len(parts)-1]
	srcs := strings.Join(parts[:len(parts)-1], " ")

	script := fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncp -a %s \"$DST\"", 
		strings.TrimPrefix(dst, "/"), srcs)

	return Instruction{
		Type:       "RUN",
		Args:       script,
		Location:   "root",
		Context:    "host",
		Conditions: []string{"all"},
	}
}

// resolveIncludePath checks relative to the current file, then the standard library.
func (b *BuildApp) resolveIncludePath(currentFile, target string) (string, error) {
	if filepath.IsAbs(target) {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}

	// 1. Relative to the file currently being parsed
	relPath := filepath.Join(filepath.Dir(currentFile), target)
	if _, err := os.Stat(relPath); err == nil { return relPath, nil }
	if _, err := os.Stat(relPath + ".dockerfile"); err == nil { return relPath + ".dockerfile", nil }

	// 2. The Standard Library (/templates)
	stdPath := filepath.Join(b.RuntimeDir, "templates", target)
	if _, err := os.Stat(stdPath); err == nil { return stdPath, nil }
	if _, err := os.Stat(stdPath + ".dockerfile"); err == nil { return stdPath + ".dockerfile", nil }

	return "", fmt.Errorf("include file not found: %s (checked relative to %s and %s/templates)", target, currentFile, b.RuntimeDir)
}

func (b *BuildApp) Parse(ctx context.Context) error {
	visited := make(map[string]bool)
	return b.parseFile(ctx, b.Dockerfile, visited)
}

func (b *BuildApp) parseFile(ctx context.Context, filePath string, visited map[string]bool) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil { return err }

	if visited[absPath] {
		return fmt.Errorf("circular INCLUDE detected: %s", absPath)
	}
	visited[absPath] = true

	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("failed to open Dockerfile %s: %w", absPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var currentBlock []string
	var isMultiline bool
	var mlType, mlLoc, mlCtx string
	var mlConds []string
	var continuedLine string

	instRegex := regexp.MustCompile(`^([A-Za-z_-]+)(?:\s*\[(.*?)\])?(?:[:]\s*|\s+)(.*)$`)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if isMultiline {
			if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				b.Instructions = append(b.Instructions, Instruction{
					Type:       mlType,
					Args:       strings.Join(currentBlock, "\n"),
					Location:   mlLoc,
					Context:    mlCtx,
					Conditions: mlConds,
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

		if !isMultiline && strings.HasSuffix(trimmed, "\\") {
			continuedLine += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		if continuedLine != "" {
			trimmed = strings.TrimSpace(continuedLine + trimmed)
			continuedLine = ""
		}

		matches := instRegex.FindStringSubmatch(trimmed)
		if matches != nil {
			baseType := strings.ToUpper(matches[1])
			modStr := matches[2]
			args := strings.TrimSpace(matches[3])
			
			loc, execCtx, conds := parseModifiers(modStr)

			if args == "|" {
				isMultiline = true
				mlType = baseType
				mlLoc = loc
				mlCtx = execCtx
				mlConds = conds
				continue
			}

			// --- RECURSIVE INCLUDE HANDLING ---
			if baseType == "INCLUDE" {
				resolvedPath, err := b.resolveIncludePath(absPath, args)
				if err != nil {
					return err
				}
				b.Log.Info(ctx, "parsing included file", "file", resolvedPath)
				if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
					return err
				}
				continue
			}

			inst := Instruction{
				Type:       baseType,
				Args:       args,
				Location:   loc,
				Context:    execCtx,
				Conditions: conds,
			}

			if inst.Type == "COPY" {
				inst = desugarCopy(inst.Args)
			}

			b.Instructions = append(b.Instructions, inst)
		}
	}

	if isMultiline && len(currentBlock) > 0 {
		b.Instructions = append(b.Instructions, Instruction{
			Type:       mlType,
			Args:       strings.Join(currentBlock, "\n"),
			Location:   mlLoc,
			Context:    mlCtx,
			Conditions: mlConds,
		})
	}

	return scanner.Err()
}

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
			tmpArgs = make(map[string]string)
			for k, v := range b.BuildArgs {
				tmpArgs[k] = v
			}
		case "NAME", "VMOUT":
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

	if len(b.Emits) == 0 {
		b.Emits = append(b.Emits, "raw")
	}

	if b.BaseImage == "" {
		return fmt.Errorf("no FROM instruction found in Dockerfile")
	}

	return nil
}

func (b *BuildApp) Substitute(val string) string {
	return os.Expand(val, func(k string) string {
		if v, ok := b.Env[k]; ok { return v }
		if v, ok := b.Args[k]; ok { return v }
		return ""
	})
}
