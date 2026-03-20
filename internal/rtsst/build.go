package rtsst

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/swiss-software-tool/sst/internal/sst"
)

type Instruction struct {
	Type       string
	Args       string
	Location   string
	Context    string
	Conditions []string
}

type BuildApp struct {
	Log          *sst.Logx
	Files        sst.Files
	ContextDir   string
	Dockerfile   string
	BaseImage    string
	Name         string
	Emits        []string
	Instructions []Instruction

	BuildArgs map[string]string
	Args      map[string]string
	Env       map[string]string

	Workdir      string
	User         string
	Cmd          string
	EntryPoint   string
	Labels       map[string]string
	Volumes      map[string]struct{}
	ExposedPorts map[string]struct{}

	BuildDir    string
	RuntimeDir  string
	RootDir     string
	ImageOutput string

	DatasetMgr  *DatasetManager
	BuildID     string
	StageMounts map[string]string
	Force       bool
}

func NewBuilder(log *sst.Logx, files sst.Files, dockerfile, contextDir, buildDir, runtimeDir string) *BuildApp {
	return &BuildApp{
		Log:          log.Subsystem("rtsst-builder"),
		Files:        files,
		ContextDir:   contextDir,
		Dockerfile:   dockerfile,
		BuildDir:     buildDir,
		RuntimeDir:   runtimeDir,
		BuildArgs:    make(map[string]string),
		Args:         make(map[string]string),
		Env:          make(map[string]string),
		Labels:       make(map[string]string),
		Volumes:      make(map[string]struct{}),
		ExposedPorts: make(map[string]struct{}),
		StageMounts:  make(map[string]string),
		Workdir:      "/",
		User:         "root",
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
		mTrimmed := strings.TrimSpace(m)
		if mTrimmed == "" {
			continue
		}

		mLower := strings.ToLower(mTrimmed)

		switch mLower {
		case "root", "base", "boot":
			loc = mLower
		case "chroot", "host":
			ctx = mLower
		default:
			if strings.HasPrefix(mTrimmed, "$") {
				loc = mTrimmed
			} else {
				parsedConds = append(parsedConds, mLower)
			}
		}
	}

	if len(parsedConds) > 0 {
		conds = parsedConds
	}
	return loc, ctx, conds
}

func desugarCopy(args string, loc string, conds []string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "COPY", Args: args}
	}

	var chown, from string
	var realParts []string

	for _, p := range parts {
		if strings.HasPrefix(p, "--chown=") {
			chown = strings.TrimPrefix(p, "--chown=")
		} else if strings.HasPrefix(p, "--from=") {
			from = strings.TrimPrefix(p, "--from=")
		} else {
			realParts = append(realParts, p)
		}
	}

	if len(realParts) < 2 {
		return Instruction{Type: "COPY", Args: args}
	}

	dst := realParts[len(realParts)-1]
	srcs := realParts[:len(realParts)-1]

	var modifiedSrcs []string
	for _, s := range srcs {
		if from != "" {
			modifiedSrcs = append(modifiedSrcs, fmt.Sprintf("\"${BASEDIR}/as-roots/%s/%s\"", from, strings.TrimPrefix(s, "/")))
		} else {
			modifiedSrcs = append(modifiedSrcs, fmt.Sprintf("\"%s\"", s))
		}
	}

	script := fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncp -a %s \"$DST\"",
		strings.TrimPrefix(dst, "/"), strings.Join(modifiedSrcs, " "))

	if chown != "" {
		script += fmt.Sprintf("\nchown -R %s \"$DST\"", chown)
	}

	return Instruction{Type: "RUN", Args: script, Location: loc, Context: "host", Conditions: conds}
}

func desugarAdd(args string, loc string, conds []string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "ADD", Args: args}
	}

	var chown string
	var realParts []string

	for _, p := range parts {
		if strings.HasPrefix(p, "--chown=") {
			chown = strings.TrimPrefix(p, "--chown=")
		} else {
			realParts = append(realParts, p)
		}
	}

	if len(realParts) < 2 {
		return Instruction{Type: "ADD", Args: args}
	}

	dst := realParts[len(realParts)-1]
	src := realParts[0]

	var script string
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncurl -sSL '%s' -o \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	} else if strings.HasSuffix(src, ".tar.gz") || strings.HasSuffix(src, ".tgz") {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$DST\"\ntar -xzf '%s' -C \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	} else {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncp -a '%s' \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	}

	if chown != "" {
		script += fmt.Sprintf("\nchown -R %s \"$DST\"", chown)
	}

	return Instruction{Type: "RUN", Args: script, Location: loc, Context: "host", Conditions: conds}
}

func (b *BuildApp) resolveIncludePath(currentFile, target string) (string, error) {
	if filepath.IsAbs(target) {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
		return "", fmt.Errorf("absolute include file not found: %s", target)
	}

	targetWithExt := target
	if !strings.HasSuffix(targetWithExt, ".dockerfile") {
		targetWithExt += ".dockerfile"
	}

	localPathExact := filepath.Join(b.ContextDir, target)
	localPathExt := filepath.Join(b.ContextDir, targetWithExt)

	var foundLocal string
	var localInfo os.FileInfo
	if info, err := os.Stat(localPathExact); err == nil {
		foundLocal = localPathExact
		localInfo = info
	} else if info, err := os.Stat(localPathExt); err == nil {
		foundLocal = localPathExt
		localInfo = info
	}

	stdDir := "/usr/local/share/mkimg/templates"
	stdPathExact := filepath.Join(stdDir, target)
	stdPathExt := filepath.Join(stdDir, targetWithExt)

	var foundStd string
	var stdInfo os.FileInfo
	if info, err := os.Stat(stdPathExact); err == nil {
		foundStd = stdPathExact
		stdInfo = info
	} else if info, err := os.Stat(stdPathExt); err == nil {
		foundStd = stdPathExt
		stdInfo = info
	}

	if foundLocal != "" && foundStd != "" {
		if stdInfo.ModTime().After(localInfo.ModTime()) {
			if b.Force {
				b.Log.Info(context.Background(), "overwriting stale local template with newer system template (--force)", "file", foundLocal)
				if err := CopyFile(foundStd, foundLocal); err != nil {
					return "", fmt.Errorf("failed to update stale template: %w", err)
				}
			} else {
				b.Log.Warn(context.Background(), "local template is older than the system template. Use --force to update it.", "file", foundLocal)
			}
		}
		return foundLocal, nil
	}

	if foundLocal != "" {
		return foundLocal, nil
	}

	if foundStd != "" {
		frozenPath := localPathExt
		if filepath.Ext(foundStd) != ".dockerfile" {
			frozenPath = localPathExact
		}

		if err := os.MkdirAll(filepath.Dir(frozenPath), 0755); err != nil {
			return "", fmt.Errorf("failed to create directory for frozen template: %w", err)
		}

		if err := CopyFile(foundStd, frozenPath); err != nil {
			return "", fmt.Errorf("failed to freeze system template to workspace: %w", err)
		}

		b.Log.Info(context.Background(), "froze system template into local workspace", "src", foundStd, "dst", frozenPath)
		return frozenPath, nil
	}

	return "", fmt.Errorf("include file not found locally or in system templates: %s", target)
}

func (b *BuildApp) Parse(ctx context.Context) error {
	visited := make(map[string]bool)

	if !filepath.IsAbs(b.Dockerfile) {
		if _, err := os.Stat(b.Dockerfile); os.IsNotExist(err) {
			if stdPath, err := b.resolveIncludePath("", b.Dockerfile); err == nil {
				b.Dockerfile = stdPath
			}
		}
	}

	return b.parseFile(ctx, b.Dockerfile, visited)
}

func (b *BuildApp) parseFile(ctx context.Context, filePath string, visited map[string]bool) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}

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
					Type: mlType, Args: strings.Join(currentBlock, "\n"), Location: mlLoc, Context: mlCtx, Conditions: mlConds,
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

			if baseType == "EMIT" {
				b.Instructions = append(b.Instructions, Instruction{Type: baseType, Args: args})
				formats := strings.Split(args, ",")
				for _, f := range formats {
					format := strings.TrimSpace(f)
					resolvedPath, err := b.resolveIncludePath(absPath, "emit/"+format)
					if err != nil {
						resolvedPath, err = b.resolveIncludePath(absPath, "emit/"+format)
					}
					if err == nil {
						b.Log.Info(ctx, "desugaring EMIT to INCLUDE", "format", format, "file", resolvedPath)
						if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
							return err
						}
					} else {
						b.Log.Warn(ctx, "no template found for EMIT format", "format", format)
					}
				}
				continue
			}

			if baseType == "FROM" {
				parts := strings.Split(args, " AS ")
				source := strings.TrimSpace(parts[0])
				asName := "root"

				if len(parts) == 2 {
					asName = strings.TrimSpace(parts[1])
					b.Instructions = append(b.Instructions, Instruction{
						Type:       "RUN",
						Args:       fmt.Sprintf("sys.provision_dataset --target=%s", asName),
						Location:   "root",
						Context:    "host",
						Conditions: []string{"all"},
					})
				} else {
					b.Instructions = append(b.Instructions, Instruction{Type: baseType, Args: source})
				}

				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "FROM_SOURCE=" + source})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "TARGET_STAGE=" + asName})

				resolvedPath, err := b.resolveIncludePath(absPath, "pull/oci")

				if err == nil {
					b.Log.Info(ctx, "desugaring FROM to INCLUDE", "file", resolvedPath)
					if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
						return err
					}
				}
				continue
			}

			if baseType == "GET" {
				parts := strings.Fields(args)
				if len(parts) < 2 {
					b.Instructions = append(b.Instructions, Instruction{Type: "GET", Args: args})
					continue
				}

				targetLoc := loc
				if targetLoc == "" || targetLoc == "root" {
					targetLoc = "root"
				}

				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "GET_URL=" + parts[0]})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "GET_DEST=" + strings.Join(parts[1:], " ")})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "TARGET_STAGE=" + targetLoc})

				resolvedPath, err := b.resolveIncludePath(absPath, "pull/get")

				if err == nil {
					b.Log.Info(ctx, "desugaring GET to INCLUDE", "file", resolvedPath)
					if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
						return err
					}
				}
				continue
			}

			if baseType == "RUN" {
				for strings.HasPrefix(args, "--mount=") || strings.HasPrefix(args, "--network=") || strings.HasPrefix(args, "--security=") {
					idx := strings.Index(args, " ")
					if idx == -1 {
						args = ""
						break
					}
					args = strings.TrimSpace(args[idx:])
				}
			}

			inst := Instruction{Type: baseType, Args: args, Location: loc, Context: execCtx, Conditions: conds}
			if inst.Type == "COPY" {
				inst = desugarCopy(inst.Args, loc, conds)
			} else if inst.Type == "ADD" {
				inst = desugarAdd(inst.Args, loc, conds)
			}
			b.Instructions = append(b.Instructions, inst)
		}
	}

	if isMultiline && len(currentBlock) > 0 {
		b.Instructions = append(b.Instructions, Instruction{
			Type: mlType, Args: strings.Join(currentBlock, "\n"), Location: mlLoc, Context: mlCtx, Conditions: mlConds,
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
			if v, ok := tmpEnv[k]; ok {
				return v
			}
			if v, ok := tmpArgs[k]; ok {
				return v
			}
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
		if v, ok := b.Env[k]; ok {
			return v
		}
		if v, ok := b.Args[k]; ok {
			return v
		}
		return ""
	})
}
