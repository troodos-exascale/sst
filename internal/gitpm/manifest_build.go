package gitpm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/swiss-software-tool/sst/internal/sst"
	"mvdan.cc/sh/v3/syntax"
)

type BuildStep struct {
	Run string `yaml:"run"`
	Dir string `yaml:"dir,omitempty"`
}

type BuildSessionOptions struct {
	List   bool
	Cont   bool
	Clean  bool
	Revert string
	Debug  bool
}

type ASTNode struct {
	Phase   string
	Index   int
	Command string
	Dir     string
	Hash    string
}

func BuildFromManifest(ctx context.Context, paths []string, opts *BuildSessionOptions) error {
	log := sst.LogxRoot().Subsystem("gitpm.dag")
	for _, p := range paths {
		m, err := LoadManifest(p)
		if err != nil {
			return err
		}
		if err := buildOneManifest(ctx, log, m, opts); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

// --- Universal Checkpointing Logic ---

func isZfsDataset(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	out, err := exec.Command("zfs", "list", "-H", "-o", "mountpoint", abs).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == abs
}

func takeSnapshot(workDir, hash string) error {
	if isZfsDataset(workDir) {
		ds, _ := exec.Command("zfs", "list", "-H", "-o", "name", workDir).Output()
		dataset := strings.TrimSpace(string(ds))
		return exec.Command("zfs", "snapshot", fmt.Sprintf("%s@%s", dataset, hash)).Run()
	}

	checkpointDir := filepath.Join(".checkpoints", filepath.Base(workDir), hash)
	_ = os.MkdirAll(filepath.Dir(checkpointDir), 0755)
	_ = os.RemoveAll(checkpointDir)
	return exec.Command("cp", "-a", workDir, checkpointDir).Run()
}

func rollbackSnapshot(workDir, hash string) error {
	if isZfsDataset(workDir) {
		ds, _ := exec.Command("zfs", "list", "-H", "-o", "name", workDir).Output()
		dataset := strings.TrimSpace(string(ds))
		out, err := exec.Command("zfs", "rollback", "-r", "-f", fmt.Sprintf("%s@%s", dataset, hash)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("zfs rollback failed: %s", string(out))
		}
		return nil
	}

	checkpointDir := filepath.Join(".checkpoints", filepath.Base(workDir), hash)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		return fmt.Errorf("checkpoint %s not found", checkpointDir)
	}

	_ = os.RemoveAll(workDir)
	return exec.Command("cp", "-a", checkpointDir, workDir).Run()
}

func cleanSnapshots(workDir string, hashes []string) {
	if isZfsDataset(workDir) {
		ds, _ := exec.Command("zfs", "list", "-H", "-o", "name", workDir).Output()
		dataset := strings.TrimSpace(string(ds))
		for _, h := range hashes {
			_ = exec.Command("zfs", "destroy", fmt.Sprintf("%s@%s", dataset, h)).Run()
		}
	} else {
		_ = os.RemoveAll(filepath.Join(".checkpoints", filepath.Base(workDir)))
	}
}

func checkSnapshotExists(workDir, hash string) bool {
	if isZfsDataset(workDir) {
		ds, _ := exec.Command("zfs", "list", "-H", "-o", "name", workDir).Output()
		dataset := strings.TrimSpace(string(ds))
		return exec.Command("zfs", "list", "-t", "snapshot", fmt.Sprintf("%s@%s", dataset, hash)).Run() == nil
	}
	checkpointDir := filepath.Join(".checkpoints", filepath.Base(workDir), hash)
	_, err := os.Stat(checkpointDir)
	return err == nil
}

// --- Main Engine ---

func buildOneManifest(ctx context.Context, log *sst.Logx, m *Manifest, opts *BuildSessionOptions) error {
	if m.Kind != "" && m.Kind != "package" {
		return fmt.Errorf("kind %q not implemented", m.Kind)
	}

	buildBase := os.Getenv("SST_PACKAGE_BUILD_DIR")
	if buildBase == "" {
		if info, err := os.Stat("/package-build"); err == nil && info.IsDir() {
			buildBase = "/package-build"
		} else {
			buildBase = "buildroot" 
		}
	}

	workDir := filepath.Join(buildBase, m.Name)
	_ = os.MkdirAll(workDir, 0o755)

	nodes := generateASTNodes(m, workDir)

	statePath := filepath.Join(buildBase, fmt.Sprintf(".%s-state.json", m.Name))

	if opts.Clean {
		fmt.Printf("🧹 Cleaning all checkpoints for %s...\n", m.Name)
		var hashes []string
		for _, n := range nodes {
			hashes = append(hashes, n.Hash)
		}
		cleanSnapshots(workDir, hashes)
		_ = os.Remove(statePath)
		fmt.Println("✅ Clean complete.")
		return nil
	}

	runner := &sst.Runner{
		Log:       log,
		StatePath: statePath,
		State:     make(map[string]interface{}),
		Completed: make(map[string]bool),
		Debug:     opts.Debug,
	}

	_ = runner.Run(ctx, &sst.Context{Action: func(c context.Context) (interface{}, error) { return nil, nil }})

	if opts.List {
		printASTList(nodes, runner.Completed)
		return nil
	}

	if opts.Revert != "" {
		return executeRevert(nodes, runner.Completed, workDir, statePath, opts.Revert)
	}

	if !opts.Cont {
		_ = os.RemoveAll(workDir)
		_ = os.MkdirAll(workDir, 0o755)
		runner.Completed = make(map[string]bool)
	}

	var root *sst.Context
	var prev *sst.Context
	var parentHash string

	for _, n := range nodes {
		currNode := n
		pHash := parentHash

		stepCtx := &sst.Context{
			ID:          currNode.Hash,
			Description: fmt.Sprintf("[%s %02d] %s", currNode.Phase, currNode.Index, currNode.Command),

			Probe: func(c context.Context) (bool, error) {
				return checkSnapshotExists(workDir, currNode.Hash), nil
			},

			Restore: func(c context.Context) error {
				if pHash != "" {
					fmt.Printf("⏪ Divergence detected. Rolling back environment to @%s...\n", pHash)
					if err := rollbackSnapshot(workDir, pHash); err != nil {
						return err
					}
				}
				return nil
			},

			Action: func(c context.Context) (interface{}, error) {
				return nil, executeASTNode(c, log, currNode, workDir)
			},
		}

		if root == nil {
			root = stepCtx
		}

		if prev != nil {
			nextCtx := stepCtx
			prevHashForSnapshot := pHash 
			prev.Interpret = func(res *sst.Result) ([]*sst.Context, error) {
				if res.Error != nil {
					return nil, res.Error
				}
				// CRITICAL FIX: Only snapshot if it doesn't already exist.
				// This prevents skipping steps from overwriting the clean historical state!
				if !checkSnapshotExists(workDir, prevHashForSnapshot) {
					_ = takeSnapshot(workDir, prevHashForSnapshot)
				}
				return []*sst.Context{nextCtx}, nil
			}
		}

		prev = stepCtx
		parentHash = currNode.Hash
	}

	if prev != nil {
		prevHashForSnapshot := parentHash 
		prev.Interpret = func(res *sst.Result) ([]*sst.Context, error) {
			if res.Error != nil {
				return nil, res.Error
			}
			if !checkSnapshotExists(workDir, prevHashForSnapshot) {
				_ = takeSnapshot(workDir, prevHashForSnapshot)
			}
			return nil, packageArtifacts(m, workDir)
		}
	}

	fmt.Printf("▶️  Executing %s %s\n", m.Name, m.Version)
	return runner.Run(ctx, root)
}

func printASTList(nodes []ASTNode, completed map[string]bool) {
	fmt.Printf("--- Build Execution Graph ---\n")
	currentPhase := ""
	for _, n := range nodes {
		if n.Phase != currentPhase {
			fmt.Printf("\nStage: [%s]\n", strings.ToUpper(n.Phase))
			currentPhase = n.Phase
		}
		status := "PENDING"
		if completed[n.Hash] {
			status = "OK     "
		}

		cmdPreview := n.Command
		if len(cmdPreview) > 60 {
			cmdPreview = cmdPreview[:57] + "..."
		}

		fmt.Printf("  [%02d] %s | %s\n", n.Index, status, cmdPreview)
	}
}

func executeRevert(nodes []ASTNode, completed map[string]bool, workDir, statePath, revertTarget string) error {
	var targetHash string
	for _, n := range nodes {
		if fmt.Sprintf("%02d", n.Index) == revertTarget {
			targetHash = n.Hash
			break
		}
	}

	if targetHash == "" {
		return fmt.Errorf("step index '%s' not found", revertTarget)
	}

	if !completed[targetHash] {
		return fmt.Errorf("cannot revert to step %s: it has not been completed", revertTarget)
	}

	fmt.Printf("⏪ Rolling back dataset to state AFTER step %s...\n", revertTarget)
	if err := rollbackSnapshot(workDir, targetHash); err != nil {
		return err
	}

	found := false
	for _, n := range nodes {
		if found {
			delete(completed, n.Hash)
		}
		if n.Hash == targetHash {
			found = true
		}
	}

	_ = os.Remove(statePath)
	fmt.Println("✅ Revert complete. Run with --cont to resume.")
	return nil
}

func generateASTNodes(m *Manifest, workDir string) []ASTNode {
	var nodes []ASTNode
	idx := 1

	h := sha256.New()
	h.Write([]byte(m.Name + m.Version))
	prevHash := hex.EncodeToString(h.Sum(nil))

	appendPhase := func(phase string, steps []BuildStep) {
		parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
		printer := syntax.NewPrinter()

		for _, step := range steps {
			prog, err := parser.Parse(strings.NewReader(step.Run), "")
			if err != nil {
				h := sha256.New()
				h.Write([]byte(prevHash + phase + step.Run + step.Dir))
				prevHash = hex.EncodeToString(h.Sum(nil))[:12]

				nodes = append(nodes, ASTNode{
					Phase:   phase,
					Index:   idx,
					Command: step.Run,
					Dir:     step.Dir,
					Hash:    prevHash,
				})
				idx++
				continue
			}

			for _, stmt := range prog.Stmts {
				var buf bytes.Buffer
				_ = printer.Print(&buf, stmt)
				cmdStr := strings.TrimSpace(buf.String())

				if cmdStr == "" {
					continue
				}

				h := sha256.New()
				h.Write([]byte(prevHash + phase + cmdStr + step.Dir))
				prevHash = hex.EncodeToString(h.Sum(nil))[:12]

				nodes = append(nodes, ASTNode{
					Phase:   phase,
					Index:   idx,
					Command: cmdStr,
					Dir:     step.Dir,
					Hash:    prevHash,
				})
				idx++
			}
		}
	}

	if m.Source.Fetch != "" {
		appendPhase("fetch", []BuildStep{{Run: m.Source.Fetch}})
	} else if m.Source.URL != "" {
		appendPhase("fetch", []BuildStep{
			{Run: fmt.Sprintf("curl -fSL '%s' -o source.tar.gz", m.Source.URL)},
			{Run: "tar -xf source.tar.gz"},
		})
	} else if m.Source.Method == "git" {
		appendPhase("fetch", []BuildStep{
			{Run: fmt.Sprintf("git clone --depth 1 --branch %s %s .", m.Source.Tag, m.Repo)},
		})
	}

	appendPhase("build", toBuildSteps(m.Build.Steps))

	return nodes
}

func executeASTNode(ctx context.Context, log *sst.Logx, node ASTNode, workDir string) error {
	absBuildRoot, _ := filepath.Abs(workDir)
	runDir := absBuildRoot
	if node.Dir != "" {
		runDir = filepath.Join(absBuildRoot, node.Dir)
	}

	_ = os.MkdirAll(runDir, 0755)

	defaultEnv := []string{
		"SOURCE_DATE_EPOCH=0",
		"TZ=UTC",
		"LANG=C",
		"LC_ALL=C",
		"KBUILD_BUILD_TIMESTAMP=0",
		"KBUILD_BUILD_USER=nobody",
		"KBUILD_BUILD_HOST=sst",
		"LDFLAGS=-Wl,--build-id=none",
	}
	env := append(os.Environ(), defaultEnv...)
	env = append(env, "BUILDROOT="+absBuildRoot)

	req := sst.ExecRequest{
		Cmd:  "bash",
		Args: []string{"-c", node.Command},
		Dir:  runDir,
		Env:  env,
		Mode: sst.ExecForeground,
	}

	_, err := sst.Exec(ctx, log, req)
	return err
}

func packageArtifacts(m *Manifest, buildRoot string) error {
	outDir := "dist"
	_ = os.MkdirAll(outDir, 0o755)
	arch := runtime.GOARCH

	opts := Options{
		InputType:   "dir",
		Name:        m.Name,
		Version:     m.Version,
		Chdir:       buildRoot,
		Prefix:      m.Install.Prefix,
		Description: "",
	}
	if opts.Prefix == "" {
		opts.Prefix = "/"
	}

	for _, t := range []string{"deb", "rpm"} {
		opts.OutputType = t
		outFile, err := func() (string, error) {
			switch t {
			case "deb":
				return BuildDeb(&opts)
			case "rpm":
				return BuildRPM(&opts)
			default:
				return "", fmt.Errorf("unknown output type: %s", t)
			}
		}()
		if err != nil {
			return err
		}

		target := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.%s", m.Name, m.Version, arch, t))
		if err := os.Rename(outFile, target); err != nil {
			if copyErr := copyFile(outFile, target); copyErr != nil {
				return fmt.Errorf("cannot move %s -> %s: %v / %v", outFile, target, err, copyErr)
			}
		}
		fmt.Printf("📦 Artifact packaged: %s\n", target)
	}
	return nil
}

func toBuildSteps(raw interface{}) []BuildStep {
	switch v := raw.(type) {
	case []struct {
		Run string `yaml:"run"`
	}:
		out := make([]BuildStep, len(v))
		for i, s := range v {
			out[i] = BuildStep{Run: s.Run}
		}
		return out

	case []struct {
		Run string `yaml:"run"`
		Dir string `yaml:"dir,omitempty"`
	}:
		out := make([]BuildStep, len(v))
		for i, s := range v {
			out[i] = BuildStep{Run: s.Run, Dir: s.Dir}
		}
		return out

	default:
		return nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
