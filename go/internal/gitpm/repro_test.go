package bpmpkg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildDeterminism(t *testing.T) {
	dir := t.TempDir()
	demoDir := filepath.Join(dir, "demo/usr/local/bin")
	if err := os.MkdirAll(demoDir, 0755); err != nil {
		t.Fatal(err)
	}

	hello := filepath.Join(demoDir, "hello")
	script := []byte("#!/bin/sh\necho Hello world\n")
	if err := os.WriteFile(hello, script, 0755); err != nil {
		t.Fatal(err)
	}

	opts := &Options{
		InputType:   "dir",
		OutputType:  "deb",
		Name:        "demo",
		Version:     "0.1",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "Hello world test",
		License:     "CDDL",
		Chdir:       filepath.Join(dir, "demo"),
	}

	out1, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("first build failed: %v", err)
	}

	out2, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("second build failed: %v", err)
	}

	data1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data1, data2) {
		t.Fatalf("non-deterministic output: %s and %s differ", out1, out2)
	}
}
