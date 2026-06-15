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
metadata:
  name: demo
  version: 9.9.9
source:
  method: local
phases:
  build:
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
	
	// SANDBOX FIX: Override the global ~/.config/gitpm/config.yaml 
	// so the test doesn't try to write to /package-build
	cmd.Env = append(os.Environ(), 
		"SST_PACKAGE_BUILD_DIR="+filepath.Join(tmp, "buildroot"),
		"SST_PACKAGE_DIST_DIR="+filepath.Join(tmp, "dist"),
	)
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build via manifest failed: %v\n%s", err, out)
	}

	// Expect both deb and rpm outputs named by arch inferred from runtime
	arch := detectArch()
	wantDeb := filepath.Join(tmp, "dist", "demo_9.9.9_"+arch+".deb")
	wantRpm := filepath.Join(tmp, "dist", "demo_9.9.9_"+arch+".rpm")
	if _, err := os.Stat(wantDeb); err != nil {
		t.Fatalf("missing output deb: %s", wantDeb)
	}
	if _, err := os.Stat(wantRpm); err != nil {
		t.Fatalf("missing output rpm: %s", wantRpm)
	}
}

func detectArch() string {
	return "arm64"
}
