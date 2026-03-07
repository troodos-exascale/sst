package bpmpkg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// gitCloneTagAndVerify clones repo at tag into dest and verifies the tag if signed==true.
// Tag verification uses your local git's configured verification:
//   - X.509/GPG signatures are verified with your local keyring
//   - SSH signatures can be verified if GIT_ALLOWED_SIGNERS is set to a file with allowed keys
//
// Fails if verification is requested and does not succeed.
func gitCloneTagAndVerify(repoURL, tag, dest string, signed bool) error {
	if repoURL == "" || tag == "" {
		return fmt.Errorf("gitCloneTagAndVerify: repoURL and tag required")
	}
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	// Shallow clone the tag
	cmd := exec.Command("git", "clone", "--depth", "1", "--branch", tag, repoURL, dest)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}

	if !signed {
		return nil
	}

	// Verify the tag signature inside the repo
	verify := exec.Command("git", "verify-tag", tag)
	verify.Dir = dest
	// Allow SSH signature verification if user supplied allow-list
	// (git uses gpg.ssh.allowedSignersFile; map env to config for convenience)
	if allowed := os.Getenv("GIT_ALLOWED_SIGNERS"); allowed != "" {
		verify.Env = append(os.Environ(),
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=gpg.ssh.allowedSignersFile",
			"GIT_CONFIG_VALUE_0="+allowed,
		)
	}
	verify.Stdout, verify.Stderr = os.Stdout, os.Stderr
	if err := verify.Run(); err != nil {
		return fmt.Errorf("git verify-tag failed for %s: %w", tag, err)
	}
	return nil
}
