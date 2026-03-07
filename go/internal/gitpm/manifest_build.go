// pkg/bpmpkg/manifest_build.go
package bpmpkg

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildStep is the shared struct used for YAML decoding and execution.
// Backward compatible: existing manifests with only `run:` still work.
// New: optional `dir:` lets you run a step in a subdirectory of the source tree.
type BuildStep struct {
	Run string `yaml:"run"`
	Dir string `yaml:"dir,omitempty"`
}

// BuildFromManifest builds all packages described by YAML manifest(s).
func BuildFromManifest(paths []string) error {
	for _, p := range paths {
		m, err := LoadManifest(p)
		if err != nil {
			return err
		}
		if err := buildOneManifest(m); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

func buildOneManifest(m *Manifest) error {
	switch m.Kind {
	case "", "package":
		return buildPackageManifest(m)
	default:
		return fmt.Errorf("kind %q not implemented", m.Kind)
	}
}

func buildPackageManifest(m *Manifest) error {
	fmt.Printf("▶️  %s %s\n", m.Name, m.Version)

	// 1) Resolve source dir (git/archive/local/exec) with sane autodetection.
	srcDir, err := manifestFetchSource(m)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	// 2) Fresh buildroot for this package
	buildRoot := filepath.Join("buildroot", m.Name)
	_ = os.RemoveAll(buildRoot)
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		return err
	}

	// 3) Execute steps with BUILDROOT exported
	if err := manifestExecuteSteps(srcDir, buildRoot, toBuildSteps(m.Build.Steps)); err != nil {
		return fmt.Errorf("steps: %w", err)
	}

	// 4) Package into dist/ with <name>_<version>_<arch>.<deb|rpm>
	outDir := "dist"
	_ = os.MkdirAll(outDir, 0o755)
	arch := runtime.GOARCH

	opts := Options{
		InputType:   "dir",
		Name:        m.Name,
		Version:     m.Version,
		Chdir:       buildRoot,
		Prefix:      "/",
		Description: "", // keep empty until provenance/annex flow
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
			// If writer placed it elsewhere/with different name, copy
			if copyErr := copyFile(outFile, target); copyErr != nil {
				return fmt.Errorf("cannot move %s -> %s: %v / %v", outFile, target, err, copyErr)
			}
		}
		fmt.Printf("✅ built %s\n", target)
	}
	return nil
}

// manifestFetchSource returns the directory where build steps should run.
// It also tries to select the *actual* project root for wrapper repos.
func manifestFetchSource(m *Manifest) (string, error) {
	base := filepath.Join("checkouts", m.Name+"-"+m.Version)
	_ = os.RemoveAll(base)
	_ = os.MkdirAll("checkouts", 0o755)

	// Mutually exclusive URL vs Fetch
	if m.Source.URL != "" && m.Source.Fetch != "" {
		return "", fmt.Errorf("manifest invalid: both url and fetch present")
	}

	// Archive URL (tar.*, zip, etc.) – naive fetch+extract
	if m.Source.URL != "" {
		fmt.Printf("🌐 fetching: %s\n", m.Source.URL)
		out := filepath.Join("checkouts", "source.tar.gz")
		if err := runLogged(".", "curl", "-fSL", m.Source.URL, "-o", out); err != nil {
			return "", err
		}
		if err := runLogged("checkouts", "tar", "-xf", "source.tar.gz"); err != nil {
			return "", err
		}
		root := firstDir("checkouts")
		return root, nil
	}

	// Custom fetch command (user-provided)
	if m.Source.Fetch != "" {
		fmt.Printf("⚙️  custom fetch\n")
		if err := runShell(".", m.Source.Fetch); err != nil {
			return "", err
		}
		return firstDir("checkouts"), nil
	}

	// Git checkout by tag (+ optional signature verification)
	if m.Source.Method == "git" {
		if m.Repo == "" || m.Source.Tag == "" {
			return "", fmt.Errorf("git fetch requires repo and tag")
		}
		fmt.Printf("📥 cloning %s @ %s\n", m.Repo, m.Source.Tag)
		if err := gitCloneTagAndVerify(m.Repo, m.Source.Tag, base, m.Source.Signed); err != nil {
			return "", err
		}
		// Heuristics:
		// 1) If base has a Makefile, use it.
		// 2) Else, if there is exactly one subdir AND it has a Makefile, descend.
		if fileExists(filepath.Join(base, "Makefile")) {
			return base, nil
		}
		if sub, ok := singleSubdir(base); ok && fileExists(filepath.Join(sub, "Makefile")) {
			return sub, nil
		}
		// Fallback to base
		return base, nil
	}

	// Local source: treat repo "", ".", or "local" as current directory
	if m.Source.Method == "local" {
		if m.Repo == "" || m.Repo == "." || m.Repo == "local" {
			return ".", nil
		}
		return m.Repo, nil
	}

	return "", fmt.Errorf("no valid source")
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

func manifestExecuteSteps(srcDir, buildRoot string, steps []BuildStep) error {
	// Ensure BUILDROOT exists and is absolute for clarity in logs
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		return err
	}
	absBuildRoot, _ := filepath.Abs(buildRoot)
	// Default reproducible build environment
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
	env = append(env, "BUILDROOT="+buildRoot)

	for i, s := range steps {
		// Resolve working directory for this step
		runDir := srcDir
		if strings.TrimSpace(s.Dir) != "" {
			runDir = filepath.Join(srcDir, s.Dir)
		}
		absRunDir, _ := filepath.Abs(runDir)

		// Expand the visible command (so logs show where $BUILDROOT ended up)
		runCmd := strings.ReplaceAll(s.Run, "$BUILDROOT", absBuildRoot)

		fmt.Printf("🔧 step %d: %s\n", i+1, s.Run)
		fmt.Printf("📂 running in %s with BUILDROOT=%s\n", absRunDir, absBuildRoot)
		fmt.Printf("🧾 expanded command: %s\n", runCmd)

		cmd := exec.Command("bash", "-c", runCmd)
		cmd.Dir = absRunDir
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("step %d failed: %w", i+1, err)
		}
	}
	return nil
}

// --- helpers ---

func runLogged(dir string, cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Dir = dir
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func runShell(dir, script string) error {
	return runShellEnv(dir, script, os.Environ())
}

func runShellEnv(dir, script string, env []string) error {
	c := exec.Command("bash", "-c", script)
	c.Dir = dir
	c.Env = env
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func firstDir(base string) string {
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return base
}

func singleSubdir(base string) (string, bool) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return "", false
	}
	var sub string
	for _, e := range ents {
		if e.IsDir() {
			if sub != "" {
				return "", false // more than one
			}
			sub = filepath.Join(base, e.Name())
		} else {
			// there are files at top-level; not a pure wrapper
			return "", false
		}
	}
	if sub == "" {
		return "", false
	}
	return sub, true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
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
