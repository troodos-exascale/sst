package rtsst

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/troodos-exascale/sst/internal/sst"
)

type Instruction struct {
	Index      int
	Type       string
	Args       string
	Location   string
	Context    string
	Conditions []string
	Hash       string

	// Frozen State (Populated during PreProcess for execution safety)
	Env         map[string]string
	Workdir     string
	User        string
	StageMounts map[string]string
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
	IncludedFiles map[string]string

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
	Force       bool
}

func NewBuilder(log *sst.Logx, files sst.Files, dockerfile, contextDir, buildDir, runtimeDir string) *BuildApp {
	return &BuildApp{
		Log:           log.Subsystem("rtsst-builder"),
		Files:         files,
		ContextDir:    contextDir,
		Dockerfile:    dockerfile,
		BuildDir:      buildDir,
		RuntimeDir:    runtimeDir,
		IncludedFiles: make(map[string]string),
		BuildArgs:     make(map[string]string),
		Args:          make(map[string]string),
		Env:           make(map[string]string),
		Labels:        make(map[string]string),
		Volumes:       make(map[string]struct{}),
		ExposedPorts:  make(map[string]struct{}),
		Workdir:       "/",
		User:          "root",
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

	if _, err := os.Stat(localPathExact); err == nil {
		return localPathExact, nil
	}
	if _, err := os.Stat(localPathExt); err == nil {
		return localPathExt, nil
	}

	stdDir := "/usr/local/share/mkimg/templates"
	stdPathExact := filepath.Join(stdDir, target)
	stdPathExt := filepath.Join(stdDir, targetWithExt)

	if _, err := os.Stat(stdPathExact); err == nil {
		return stdPathExact, nil
	}
	if _, err := os.Stat(stdPathExt); err == nil {
		return stdPathExt, nil
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
	defer delete(visited, absPath)

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
				b.IncludedFiles[args] = resolvedPath
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
					relPath := "emit/" + format
					resolvedPath, err := b.resolveIncludePath(absPath, relPath)
					if err == nil {
						b.IncludedFiles[relPath] = resolvedPath
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

				if source == "scratch" {
					continue
				}

				relPath := "pull/oci"
				resolvedPath, err := b.resolveIncludePath(absPath, relPath)

				if err == nil {
					b.IncludedFiles[relPath] = resolvedPath
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

				relPath := "pull/get"
				resolvedPath, err := b.resolveIncludePath(absPath, relPath)

				if err == nil {
					b.IncludedFiles[relPath] = resolvedPath
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

func cloneMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// PreProcess executes Pass 1: State accumulation and Hash computation.
func (b *BuildApp) PreProcess(ctx context.Context) error {
	// Active running state
	activeArgs := make(map[string]string)
	for k, v := range b.BuildArgs {
		activeArgs[k] = v
	}
	activeEnv := make(map[string]string)
	activeWorkdir := "/"
	activeUser := "root"
	activeStageMounts := make(map[string]string)

	substitute := func(val string) string {
		return os.Expand(val, func(k string) string {
			if v, ok := activeEnv[k]; ok { return v }
			if v, ok := activeArgs[k]; ok { return v }
			return ""
		})
	}

	h := sha256.New()
	
	// Pre-seed the hash with build args to ensure different args yield different graphs
	var keys []string
	for k := range b.BuildArgs { keys = append(keys, k) }
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k + "=" + b.BuildArgs[k]))
	}

	prevHash := hex.EncodeToString(h.Sum(nil))

	for i := range b.Instructions {
		inst := &b.Instructions[i]
		inst.Index = i

		args := substitute(inst.Args)
		cleanArgs := strings.SplitN(args, " #", 2)[0]
		cleanArgs = strings.TrimSpace(cleanArgs)

		// State Accumulation
		switch inst.Type {
		case "ARG":
			parts := strings.SplitN(cleanArgs, "=", 2)
			if len(parts) == 2 {
				activeArgs[parts[0]] = parts[1]
			}
		case "ENV":
			parts := strings.SplitN(cleanArgs, "=", 2)
			if len(parts) == 2 {
				activeEnv[parts[0]] = parts[1]
			} else {
				parts = strings.SplitN(cleanArgs, " ", 2)
				if len(parts) == 2 {
					activeEnv[parts[0]] = parts[1]
				}
			}
		case "FROM":
			if b.BaseImage == "" {
				b.BaseImage = cleanArgs
			}
			activeArgs = make(map[string]string)
			for k, v := range b.BuildArgs { activeArgs[k] = v }
		case "WORKDIR":
			activeWorkdir = cleanArgs
		case "USER":
			activeUser = cleanArgs
		case "CMD":
			b.Cmd = substitute(cleanArgs)
		case "ENTRYPOINT":
			b.EntryPoint = substitute(cleanArgs)
		case "LABEL":
			parts := strings.SplitN(cleanArgs, "=", 2)
			if len(parts) == 2 {
				b.Labels[strings.Trim(parts[0], `"`)] = strings.Trim(parts[1], `"`)
			}
		case "EXPOSE":
			for _, port := range strings.Fields(cleanArgs) {
				if !strings.Contains(port, "/") {
					port += "/tcp"
				}
				b.ExposedPorts[port] = struct{}{}
			}
		case "VOLUME":
			clean := strings.TrimPrefix(strings.TrimSuffix(cleanArgs, "]"), "[")
			for _, vol := range strings.Split(clean, ",") {
				b.Volumes[strings.Trim(strings.TrimSpace(vol), `"`)] = struct{}{}
			}
		case "NAME", "VMOUT":
			b.Name = cleanArgs
		case "EMIT":
			emits := strings.Split(cleanArgs, ",")
			for _, e := range emits {
				b.Emits = append(b.Emits, strings.ToLower(strings.TrimSpace(e)))
			}
		case "RUN":
			if strings.HasPrefix(inst.Args, "sys.provision_dataset") {
				parts := strings.Fields(inst.Args)
				for _, p := range parts {
					if strings.HasPrefix(p, "--target=") {
						target := strings.TrimPrefix(p, "--target=")
						// We map the expected future directory path 
						activeStageMounts[target] = fmt.Sprintf("as-roots/%s", target)
					}
				}
			}
		}

		// Snapshot freezing
		inst.Env = cloneMap(activeEnv)
		for k, v := range activeArgs {
			inst.Env[k] = v // Merge args into env snapshot for execution context
		}
		inst.Workdir = activeWorkdir
		inst.User = activeUser
		inst.StageMounts = cloneMap(activeStageMounts)

		// Hashing
		h := sha256.New()
		h.Write([]byte(prevHash))
		h.Write([]byte(inst.Type))
		h.Write([]byte(inst.Args))
		h.Write([]byte(inst.Context))
		h.Write([]byte(inst.Location))
		h.Write([]byte(strings.Join(inst.Conditions, ",")))

		prevHash = hex.EncodeToString(h.Sum(nil))
		inst.Hash = prevHash
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
