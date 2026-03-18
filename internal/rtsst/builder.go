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

	RuntimeDir  string
	RootDir     string
	ImageOutput string

	DatasetMgr  *DatasetManager
	BuildID     string
	StageMounts map[string]string
	Force       bool
}

func NewBuilder(log *sst.Logx, files sst.Files, dockerfile, runtimeDir string) *BuildApp {
	return &BuildApp{
		Log:          log.Subsystem("rtsst-builder"),
		Files:        files,
		Dockerfile:   dockerfile,
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
			// If the modifier is a variable, treat it as a dynamic location and PRESERVE CASE
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

// desugarFrom generates a pure shell script to handle OCI caching and extraction.
// This allows the Location modifier to dynamically route the extraction into AS stages.
func desugarFrom(source, loc string) Instruction {
	script := fmt.Sprintf(`set -e
if [ "%s" = "scratch" ] || [ -z "%s" ]; then exit 0; fi

ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
echo "--> Resolving strict OCI digest for linux/${ARCH}..."
DIGEST=$(crane digest --platform "linux/${ARCH}" "%s")

SAFE_NAME=$(echo "%s@${DIGEST}" | tr ':/@' '_')
CACHE_TAR="${SST_CACHE_FROM}/${SAFE_NAME}.tar"

if [ ! -f "$CACHE_TAR" ]; then
	echo "--> FROM cache miss: fetching immutable digest..."
	crane export "%s@${DIGEST}" "$CACHE_TAR"
else
	echo "--> FROM cache hit."
fi

echo "--> Extracting filesystem to ${ROOTFS}..."
mkdir -p "${ROOTFS}"
tar --numeric-owner -C "${ROOTFS}" -xf "$CACHE_TAR"
find "${ROOTFS}" -exec chown -h 0:0 {} +
find "${ROOTFS}" -exec touch -h -d @0 {} +
`, source, source, source, source, source)

	return Instruction{Type: "RUN", Args: script, Location: loc, Context: "host", Conditions: []string{"all"}}
}

// desugarGet generates a pure shell script for cached network downloads.
func desugarGet(args, loc string, conds []string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "GET", Args: args} // Fallback to failure
	}
	url := parts[0]
	dst := parts[len(parts)-1]

	script := fmt.Sprintf(`set -e
URL='%s'
DST="${ROOTFS}/%s"

HASH=$(echo -n "$URL" | sha256sum | awk '{print $1}')
CACHE_FILE="${SST_CACHE_GET}/${HASH}"

if [ ! -f "$CACHE_FILE" ]; then
	echo "--> GET cache miss: downloading payload..."
	curl -sSL "$URL" -o "$CACHE_FILE"
else
	echo "--> GET cache hit."
fi

if [ "${DST: -1}" = "/" ]; then
	DST="${DST}$(basename "$URL" | cut -d? -f1)"
fi

mkdir -p "$(dirname "$DST")"
cp -a "$CACHE_FILE" "$DST"
`, url, strings.TrimPrefix(dst, "/"))

	return Instruction{Type: "RUN", Args: script, Location: loc, Context: "host", Conditions: conds}
}

func desugarCopy(args string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "COPY", Args: args}
	}
	dst := parts[len(parts)-1]
	srcs := strings.Join(parts[:len(parts)-1], " ")
	script := fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncp -a %s \"$DST\"", strings.TrimPrefix(dst, "/"), srcs)
	return Instruction{Type: "RUN", Args: script, Location: "root", Context: "host", Conditions: []string{"all"}}
}

func desugarAdd(args string) Instruction {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return Instruction{Type: "ADD", Args: args}
	}
	dst := parts[len(parts)-1]
	src := parts[0]
	var script string
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncurl -sSL '%s' -o \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	} else if strings.HasSuffix(src, ".tar.gz") || strings.HasSuffix(src, ".tgz") {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$DST\"\ntar -xzf '%s' -C \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	} else {
		script = fmt.Sprintf("DST=\"${ROOTFS}/%s\"\nmkdir -p \"$(dirname \"$DST\")\"\ncp -a '%s' \"$DST\"", strings.TrimPrefix(dst, "/"), src)
	}
	return Instruction{Type: "RUN", Args: script, Location: "root", Context: "host", Conditions: []string{"all"}}
}

func (b *BuildApp) resolveIncludePath(currentFile, target string) (string, error) {
	if filepath.IsAbs(target) {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	relPath := filepath.Join(filepath.Dir(currentFile), target)
	if _, err := os.Stat(relPath); err == nil {
		return relPath, nil
	}
	if _, err := os.Stat(relPath + ".dockerfile"); err == nil {
		return relPath + ".dockerfile", nil
	}
	stdPath := filepath.Join("/usr/local/share/mkimg/templates", target)
	if _, err := os.Stat(stdPath); err == nil {
		return stdPath, nil
	}
	if _, err := os.Stat(stdPath + ".dockerfile"); err == nil {
		return stdPath + ".dockerfile", nil
	}
	return "", fmt.Errorf("include file not found: %s", target)
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
				b.Instructions = append(b.Instructions, Instruction{Type: mlType, Args: strings.Join(currentBlock, "\n"), Location: mlLoc, Context: mlCtx, Conditions: mlConds})
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
					resolvedPath, err := b.resolveIncludePath(absPath, "emit-"+format)
					if err == nil {
						b.Log.Info(ctx, "desugaring EMIT to INCLUDE", "format", format, "file", resolvedPath)
						if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
							return err
						}
					}
				}
				continue
			}

			// Inside parseFile(...) in internal/rtsst/builder.go

			// --- DESUGAR FROM TO DATASET PROVISIONING & TEMPLATE ---
			if baseType == "FROM" {
				parts := strings.Split(args, " AS ")
				source := strings.TrimSpace(parts[0])
				asName := "root" // Default location if no AS is provided

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

				// Inject parameters for the template to consume
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "FROM_SOURCE=" + source})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "TARGET_STAGE=" + asName})

				resolvedPath, err := b.resolveIncludePath(absPath, "from-oci")
				if err == nil {
					b.Log.Info(ctx, "desugaring FROM to INCLUDE", "file", resolvedPath)
					if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
						return err
					}
				}
				continue
			}

			// --- DESUGAR GET TO TEMPLATE ---
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

				// Inject parameters for the template
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "GET_URL=" + parts[0]})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "GET_DEST=" + strings.Join(parts[1:], " ")})
				b.Instructions = append(b.Instructions, Instruction{Type: "ARG", Args: "TARGET_STAGE=" + targetLoc})

				resolvedPath, err := b.resolveIncludePath(absPath, "get")
				if err == nil {
					b.Log.Info(ctx, "desugaring GET to INCLUDE", "file", resolvedPath)
					if err := b.parseFile(ctx, resolvedPath, visited); err != nil {
						return err
					}
				}
				continue
			}

			inst := Instruction{Type: baseType, Args: args, Location: loc, Context: execCtx, Conditions: conds}
			if inst.Type == "COPY" {
				inst = desugarCopy(inst.Args)
			} else if inst.Type == "ADD" {
				inst = desugarAdd(inst.Args)
			}
			b.Instructions = append(b.Instructions, inst)
		}
	}

	if isMultiline && len(currentBlock) > 0 {
		b.Instructions = append(b.Instructions, Instruction{Type: mlType, Args: strings.Join(currentBlock, "\n"), Location: mlLoc, Context: mlCtx, Conditions: mlConds})
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
