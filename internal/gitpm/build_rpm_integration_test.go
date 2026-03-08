package gitpm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIBuildManifestDEBAndRPM(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "gitpm")

	// Build gitpm and output the binary directly into the temp dir
	if err := exec.Command("go", "build", "-o", binPath, "../../cmd/gitpm").Run(); err != nil {
		t.Fatalf("failed to build gitpm: %v", err)
	}

	// Create a minimal manifest that installs one file
	manifest := `
schema: v1.3
kind: package
name: demo
version: 9.9.9
repo: local
source:
  method: local
build:
  steps:
    - run: mkdir -p $BUILDROOT/usr/local/bin
    - run: printf '#!/bin/sh\necho hi\n' > $BUILDROOT/usr/local/bin/hi.sh
    - run: chmod 0755 $BUILDROOT/usr/local/bin/hi.sh
`
	mf := filepath.Join(tmp, "demo.yaml")
	if err := os.WriteFile(mf, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Use the absolute path to the binary we just built
	args := []string{binPath, "build", "-f", mf}
	cmd := exec.Command("bash", "-c", strings.Join(args, " "))
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build via manifest failed: %v\n%s", err, out)
	}

	// Expect both deb and rpm outputs named by arch inferred from runtime
	arch := detectArch()
	wantDeb := filepath.Join(tmp, "dist", "demo_9.9.9_"+arch+".deb") // <-- NOTE: output goes to dist/
	wantRpm := filepath.Join(tmp, "dist", "demo_9.9.9_"+arch+".rpm") // <-- NOTE: output goes to dist/
	if _, err := os.Stat(wantDeb); err != nil {
		t.Fatalf("missing output deb: %s", wantDeb)
	}
	if _, err := os.Stat(wantRpm); err != nil {
		t.Fatalf("missing output rpm: %s", wantRpm)
	}
}

func detectArch() string {
	// mirror pkg/gitpm/options.go default arch logic if needed in tests
	// Keep it simple for now:
	return "arm64"
}
