// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	"github.com/troodos-exascale/sst/internal/sst"
	gitpmpb "github.com/troodos-exascale/sst/proto/yaml/gitpm/v1"
	"mvdan.cc/sh/v3/syntax"
	"sigs.k8s.io/yaml"
)

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

func LoadManifest(path string) (*gitpmpb.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m gitpmpb.Manifest
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest %s: %w", path, err)
	}
	// Sanity checks
	if m.Metadata == nil {
		m.Metadata = &gitpmpb.Metadata{Name: "unknown", Version: "0.0.0"}
	}
	if m.Outputs == nil {
		m.Outputs = &gitpmpb.Outputs{Prefix: "/"}
	}
	return &m, nil
}

// BuildTarget replaces BuildFromManifest. It handles both single files AND local Git directories.
func BuildTarget(ctx context.Context, target string, opts *BuildSessionOptions) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	log := sst.LogxRoot().Subsystem("gitpm.dag")
	stat, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("target not found: %s", target)
	}

	var manifestPath string
	var workDir string

	if stat.IsDir() {
		// --- LOCAL REPO MODE ---
		manifestPath = filepath.Join(target, ".gitpm", "manifest.yaml")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			return fmt.Errorf("no .gitpm/manifest.yaml found in %s", target)
		}

		m, err := LoadManifest(manifestPath)
		if err != nil {
			return err
		}

		workDir = filepath.Join(cfg.BuildDir, m.Metadata.Name)

		if !opts.Cont && !opts.Clean && opts.Revert == "" && !opts.List {
			_ = os.RemoveAll(workDir)
			_ = os.MkdirAll(filepath.Dir(workDir), 0755)

			fmt.Printf("📦 Cloning local repository into ZFS workspace: %s\n", workDir)
			// Using --local hardlinks the Git objects, making this near-instant while preserving ZFS boundaries
			cloneCmd := exec.Command("git", "clone", "--local", target, workDir)
			if out, err := cloneCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to clone into buildroot: %s\n%s", err, out)
			}
		}

		manifestPath = filepath.Join(workDir, ".gitpm", "manifest.yaml")
	} else {
		// --- STANDARD FILE MODE ---
		manifestPath = target
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}

	if workDir == "" {
		workDir = filepath.Join(cfg.BuildDir, m.Metadata.Name)
		_ = os.MkdirAll(workDir, 0755)
	}

	return buildOneManifest(ctx, log, m, workDir, cfg, opts)
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

func buildOneManifest(ctx context.Context, log *sst.Logx, m *gitpmpb.Manifest, workDir string, cfg *Config, opts *BuildSessionOptions) error {
	if m.Kind != "" && m.Kind != "package" {
		return fmt.Errorf("kind %q not implemented", m.Kind)
	}

	nodes := generateASTNodes(m, workDir)
	statePath := filepath.Join(cfg.BuildDir, fmt.Sprintf(".%s-state.json", m.Metadata.Name))

	if opts.Clean {
		var hashes []string
		for _, n := range nodes {
			hashes = append(hashes, n.Hash)
		}
		cleanSnapshots(workDir, hashes)
		_ = os.Remove(statePath)
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
		runner.Completed = make(map[string]bool)
	}

	var root *sst.Context
	var prev *sst.Context
	var parentHash string
	var hasRestored bool

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
				if opts.Cont && !hasRestored && pHash != "" {
					fmt.Printf("\n\033[33m⏪ RESUMING: Restoring environment to state @%s...\033[0m\n\n", pHash[:8])
					if err := rollbackSnapshot(workDir, pHash); err != nil {
						return err
					}
					hasRestored = true
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
			return nil, packageArtifacts(m, workDir, cfg)
		}
	}

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
	var parentHash string
	for _, n := range nodes {
		if fmt.Sprintf("%02d", n.Index) == revertTarget {
			targetHash = n.Hash
			break
		}
		parentHash = n.Hash
	}

	if targetHash == "" {
		return fmt.Errorf("step index '%s' not found", revertTarget)
	}

	if !completed[targetHash] && !completed[parentHash] {
		return fmt.Errorf("cannot revert step %s: neither it nor its parent has been completed", revertTarget)
	}

	if parentHash == "" {
		_ = os.RemoveAll(workDir)
		_ = os.MkdirAll(workDir, 0o755)
	} else {
		if err := rollbackSnapshot(workDir, parentHash); err != nil {
			return err
		}
	}

	found := false
	for _, n := range nodes {
		if n.Hash == targetHash {
			found = true
		}
		if found {
			delete(completed, n.Hash)
		}
	}

	_ = os.Remove(statePath)
	return nil
}

func generateASTNodes(m *gitpmpb.Manifest, workDir string) []ASTNode {
	var nodes []ASTNode
	idx := 1

	h := sha256.New()
	h.Write([]byte(m.Metadata.Name + m.Metadata.Version))
	prevHash := hex.EncodeToString(h.Sum(nil))

	appendPhase := func(phase string, steps []*gitpmpb.Step) {
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

	if m.Source != nil {
		if m.Source.Method == "git" {
			appendPhase("fetch", []*gitpmpb.Step{{Run: fmt.Sprintf("git clone --depth 1 --branch %s %s .", m.Source.Ref, m.Source.Url)}})
		} else if m.Source.Method == "tar" {
			appendPhase("fetch", []*gitpmpb.Step{
				{Run: fmt.Sprintf("curl -fSL '%s' -o source.tar.gz", m.Source.Url)},
				{Run: "tar -xf source.tar.gz"},
			})
		}
	}

	if m.Phases != nil {
		appendPhase("fetch", m.Phases.Fetch)
		appendPhase("build", m.Phases.Build)
		appendPhase("build_test", m.Phases.BuildTest)
		appendPhase("install", m.Phases.Install)
		appendPhase("install_test", m.Phases.InstallTest)
	}

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

func packageArtifacts(m *gitpmpb.Manifest, buildRoot string, cfg *Config) error {
	outDir := cfg.DistDir
	_ = os.MkdirAll(outDir, 0o755)
	arch := runtime.GOARCH

	opts := Options{
		InputType:   "dir",
		Name:        m.Metadata.Name,
		Version:     m.Metadata.Version,
		Chdir:       buildRoot,
		Prefix:      m.Outputs.Prefix,
		Description: m.Metadata.Description,
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

		target := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.%s", m.Metadata.Name, m.Metadata.Version, arch, t))
		if err := os.Rename(outFile, target); err != nil {
			if copyErr := copyFile(outFile, target); copyErr != nil {
				return fmt.Errorf("cannot move %s -> %s: %v / %v", outFile, target, err, copyErr)
			}
		}
		fmt.Printf("📦 Artifact packaged securely to DistDir: %s\n", target)
	}
	return nil
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
