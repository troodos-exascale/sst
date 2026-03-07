package bpmpkg

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- helpers for reading minimal .ar (deb) ---
type arMember struct {
	Name string
	Body []byte
}

func readArAll(r io.Reader) ([]arMember, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, fmt.Errorf("ar: read magic: %w", err)
	}
	if string(magic) != "!<arch>\n" {
		return nil, fmt.Errorf("ar: bad magic")
	}
	var out []arMember
	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("ar: read header: %w", err)
		}
		name := strings.TrimSpace(string(hdr[:16]))
		sizeField := strings.TrimSpace(string(hdr[48:58]))
		var size int
		fmt.Sscanf(sizeField, "%d", &size)

		body := make([]byte, size)
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, fmt.Errorf("ar: read body: %w", err)
		}
		// even-byte padding
		if size%2 != 0 {
			if _, err := br.ReadByte(); err != nil {
				return nil, fmt.Errorf("ar: pad read: %w", err)
			}
		}
		out = append(out, arMember{Name: name, Body: body})
		// Stop if debian-binary/control/data have all been seen, but it's okay to loop to EOF.
	}
	return out, nil
}

// untar gzipped tar into a map[name]header for inspection (no FS writes)
type tarEntry struct {
	H  *tar.Header
	Br []byte
}

func readGzTarAll(data []byte) (map[string]tarEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]tarEntry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if h.Size > 0 && h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(&buf, tr); err != nil {
				return nil, err
			}
		}
		out[h.Name] = tarEntry{H: h, Br: buf.Bytes()}
	}
	return out, nil
}

func sha256File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func TestCombinedDeterminismDebAndRPM(t *testing.T) {
	// Build a small tree with files + symlink + modes
	tmp := t.TempDir()
	root := filepath.Join(tmp, "tree")
	bin := filepath.Join(root, "usr/local/bin")
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	hi := filepath.Join(bin, "hi.sh")
	hi2 := filepath.Join(bin, "hi2.sh")
	if err := os.WriteFile(hi, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hi2, []byte("#!/bin/sh\necho hi2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// symlink /usr/bin-hi -> ../local/bin/hi.sh (as seen in your rpm check)
	linkPath := filepath.Join(root, "usr/bin-hi")
	if err := os.MkdirAll(filepath.Join(root, "usr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../local/bin/hi.sh", linkPath); err != nil {
		t.Fatal(err)
	}

	// Common opts (deb)
	debOpts := &Options{
		InputType:   "dir",
		OutputType:  "deb",
		Name:        "demo",
		Version:     "9.9.9",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "combined determinism test (deb)",
		Chdir:       root,
		Prefix:      "/", // install path already full under root
	}
	outDeb1, err := BuildDeb(debOpts)
	if err != nil {
		t.Fatalf("deb build1: %v", err)
	}
	outDeb2, err := BuildDeb(debOpts)
	if err != nil {
		t.Fatalf("deb build2: %v", err)
	}

	// Deb byte-for-byte equality
	b1, _ := os.ReadFile(outDeb1)
	b2, _ := os.ReadFile(outDeb2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf(".deb not byte-identical across rebuilds")
	}

	// Inspect deb data.tar.gz contents: files, modes, symlink, epoch times, root ownership
	members, err := readArAll(bytes.NewReader(b1))
	if err != nil {
		t.Fatalf("deb ar read: %v", err)
	}
	var dataTar []byte
	for _, m := range members {
		if m.Name == "data.tar.gz" {
			dataTar = m.Body
			break
		}
	}
	if dataTar == nil {
		t.Fatalf("deb missing data.tar.gz")
	}
	entries, err := readGzTarAll(dataTar)
	if err != nil {
		t.Fatalf("deb data untar: %v", err)
	}

	// Expect paths under /usr/local/bin and symlink /usr/bin-hi
	expectFiles := []struct {
		name string
		mode int64
	}{
		{"usr/local/bin/hi.sh", 0o755},
		{"usr/local/bin/hi2.sh", 0o755},
	}
	for _, e := range expectFiles {
		te, ok := entries[strings.TrimPrefix(e.name, "/")]
		if !ok {
			t.Fatalf("deb missing file: %s", e.name)
		}
		if te.H.Mode != e.mode {
			t.Fatalf("deb mode mismatch for %s: got %o want %o", e.name, te.H.Mode, e.mode)
		}
		// epoch time
		if !te.H.ModTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("deb mtime not epoch for %s: %v", e.name, te.H.ModTime)
		}
		// root:root
		if te.H.Uid != 0 || te.H.Gid != 0 {
			t.Fatalf("deb ownership not root for %s: uid=%d gid=%d", e.name, te.H.Uid, te.H.Gid)
		}
	}

	// Symlink check
	sym, ok := entries["usr/bin-hi"]
	if !ok {
		t.Fatalf("deb missing symlink usr/bin-hi")
	}
	if sym.H.Typeflag != tar.TypeSymlink {
		t.Fatalf("deb usr/bin-hi not a symlink (type=%v)", sym.H.Typeflag)
	}
	if sym.H.Linkname != "../local/bin/hi.sh" {
		t.Fatalf("deb symlink target mismatch: got %q", sym.H.Linkname)
	}
	if !sym.H.ModTime.Equal(time.Unix(0, 0)) {
		t.Fatalf("deb symlink mtime not epoch")
	}
	if sym.H.Uid != 0 || sym.H.Gid != 0 {
		t.Fatalf("deb symlink not root owned: uid=%d gid=%d", sym.H.Uid, sym.H.Gid)
	}

	// Common opts (rpm)
	rpmOpts := &Options{
		InputType:   "dir",
		OutputType:  "rpm",
		Name:        "demo",
		Version:     "9.9.9",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "combined determinism test (rpm)",
		Chdir:       root,
		Prefix:      "/", // paths already absolute under root tree
	}
	outRpm1, err := BuildRPM(rpmOpts)
	if err != nil {
		t.Fatalf("rpm build1: %v", err)
	}
	outRpm2, err := BuildRPM(rpmOpts)
	if err != nil {
		t.Fatalf("rpm build2: %v", err)
	}

	// RPM byte-for-byte equality
	r1, _ := os.ReadFile(outRpm1)
	r2, _ := os.ReadFile(outRpm2)
	if !bytes.Equal(r1, r2) {
		t.Fatalf(".rpm not byte-identical across rebuilds")
	}

	// Basic RPM sanity: lead magic 0xed 0xab 0xee 0xdb (RPM v3/v4)
	if len(r1) < 4 || !(r1[0] == 0xed && r1[1] == 0xab && r1[2] == 0xee && r1[3] == 0xdb) {
		t.Fatalf("rpm missing/invalid magic lead bytes")
	}

	// Log some hashes to help debugging if ever needed
	debHash, _ := sha256File(outDeb1)
	rpmHash, _ := sha256File(outRpm1)
	t.Logf("deb: %s (%d bytes)  rpm: %s (%d bytes)", debHash, len(b1), rpmHash, len(r1))
}
