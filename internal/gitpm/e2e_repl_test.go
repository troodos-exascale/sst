package gitpm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_Gitpm_TimeTravel_REPL(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "gitpm")

	t.Log("Compiling gitpm binary for E2E test...")
	if err := exec.Command("go", "build", "-o", binPath, "../../cmd/gitpm").Run(); err != nil {
		t.Fatalf("failed to build gitpm: %v", err)
	}

	manifestPath := filepath.Join(tmpDir, "demo-package.yaml")
	initialManifest := `schema: v1.3
kind: package
metadata:
  name: demo-pkg
  version: "1.0"
source:
  method: local
phases:
  build:
    - run: |
        echo "Step 1: Downloading huge file..."
        echo "heavy payload data" > payload.txt
    - run: |
        echo "Step 2: Processing file..."
        cat payload.txt
        echo "Simulating a typo error now..."
        exit 1
    - run: |
        echo "Step 3: Finalizing..."
        touch success.txt
`
	if err := os.WriteFile(manifestPath, []byte(initialManifest), 0644); err != nil {
		t.Fatal(err)
	}

	runCmd := func(desc string, args ...string) (string, error) {
		cmdStr := "gitpm " + strings.Join(args, " ")
		if desc != "" {
			fmt.Printf("\n\033[36m# %s\033[0m\n", desc)
		}
		fmt.Printf("\033[32m$ %s\033[0m\n", cmdStr)

		cmdArgs := append([]string{binPath}, args...)
		cmd := exec.Command("bash", "-c", strings.Join(cmdArgs, " "))
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), 
			"SST_LOG_FORMAT=text",
			"SST_PACKAGE_BUILD_DIR=buildroot",
		)
		
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		
		fmt.Print(out.String())
		return out.String(), err
	}

	fmt.Println("\n========================================================")
	fmt.Println("🎬 SCENE 1: The standard, frustrating build failure")
	fmt.Println("========================================================")

	_, err := runCmd("Run standard build", "build", "-f", "demo-package.yaml")
	if err == nil {
		t.Fatalf("Expected build to fail!")
	}

	fmt.Println("\n========================================================")
	fmt.Println("🎬 SCENE 2: The following demonstrates how gitpm makes debugging a pleasure")
	fmt.Println("========================================================")

	outList, _ := runCmd("Inspect the execution graph", "build", "-f", "demo-package.yaml", "--list")
	if !strings.Contains(outList, "[06] PENDING | exit 1") {
		t.Fatalf("Expected AST Node 06 to be the pending 'exit 1' command.")
	}

	_, _ = runCmd("Time-travel to the exact moment before failure", "build", "-f", "demo-package.yaml", "--revert", "06")

	fmt.Printf("\n\033[36m# Correct the code in the YAML manifest...\033[0m\n")
	fmt.Printf("\033[32m$ sed -i 's/.*Simulating a typo error.*/echo \"Fixed!\"/' demo-package.yaml\033[0m\n")
	fmt.Printf("\033[32m$ sed -i '/exit 1/d' demo-package.yaml\033[0m\n")
	
	badBlock := "echo \"Simulating a typo error now...\"\n        exit 1"
	goodBlock := "echo \"Fixed!\""
	fixedManifest := strings.Replace(initialManifest, badBlock, goodBlock, 1)
	_ = os.WriteFile(manifestPath, []byte(fixedManifest), 0644)

	outCont, err := runCmd("Resume execution using cached disk state", "build", "-f", "demo-package.yaml", "--cont")
	if err != nil {
		t.Fatalf("Continue command failed: %v", err)
	}
	if !strings.Contains(outCont, "Fixed!") {
		t.Fatalf("Expected successful execution of fixed command")
	}

	debPath := filepath.Join(tmpDir, "dist", "demo-pkg_1.0_arm64.deb")
	hash1, _ := fileSHA256(debPath)
	fmt.Printf("\n\033[33mSHA256 (Resumed Package): %s\033[0m\n", hash1)

	fmt.Println("\n========================================================")
	fmt.Println("🎬 SCENE 3: Now build in one blow to prove determinism")
	fmt.Println("========================================================")

	_, _ = runCmd("Clean caches and build fresh", "build", "-f", "demo-package.yaml", "--clean")
	_, err = runCmd("Build fresh", "build", "-f", "demo-package.yaml")
	if err != nil {
		t.Fatalf("Fresh build failed: %v", err)
	}

	hash2, _ := fileSHA256(debPath)
	fmt.Printf("\n\033[33mSHA256 (Fresh Package):   %s\033[0m\n", hash2)

	if hash1 != hash2 {
		t.Fatalf("Determinism failure! Hashes do not match.")
	}

	fmt.Println("\n✅ CUT! That's a wrap.")
}
