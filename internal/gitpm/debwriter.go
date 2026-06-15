package gitpm

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// BuildDeb writes a deterministic .deb package directly from buildroot/
func BuildDeb(opts *Options) (string, error) {
	outFile := fmt.Sprintf("%s_%s_%s.deb", opts.Name, opts.Version, "all")
	outPath := filepath.Join("dist", outFile)
	_ = os.MkdirAll("dist", 0o755)

	tmpDir := filepath.Join(os.TempDir(), "gitpm-deb")
	_ = os.RemoveAll(tmpDir)
	_ = os.MkdirAll(tmpDir, 0o755)

	controlTar := filepath.Join(tmpDir, "control.tar.gz")
	dataTar := filepath.Join(tmpDir, "data.tar.gz")

	if err := writeControlTar(controlTar, opts); err != nil {
		return "", err
	}
	if err := writeDataTar(dataTar, opts.Chdir); err != nil {
		return "", err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// Write minimal ar archive
	w := func(name string, data []byte) error {
		header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n",
			name, 0, 0, 0, 0, len(data))
		if _, err := out.Write([]byte(header)); err != nil {
			return err
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
		if len(data)%2 != 0 {
			out.Write([]byte{'\n'})
		}
		return nil
	}

	// ar magic is at start (exactly 8 bytes, NOT a file header)
	if _, err := out.Write([]byte("!<arch>\n")); err != nil {
		return "", err
	}

	debianBinary := []byte("2.0\n")

	w("debian-binary", debianBinary)
	if b, err := os.ReadFile(controlTar); err == nil {
		w("control.tar.gz", b)
	}
	if b, err := os.ReadFile(dataTar); err == nil {
		w("data.tar.gz", b)
	}

	fmt.Printf("✅ built %s\n", outPath)
	return outPath, nil
}

// writeControlTar creates deterministic control.tar.gz
func writeControlTar(path string, opts *Options) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	gw.Header.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gw)

	control := fmt.Sprintf(`Package: %s
Version: %s
Architecture: all
Maintainer: nobody@sst
Description: %s
`, opts.Name, opts.Version, opts.Description)

	addTarFile(tw, "control", []byte(control))

	tw.Close()
	gw.Close()
	f.Close()
	return nil
}

// writeDataTar archives contents of buildroot deterministically
func writeDataTar(path, dir string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	gw.Header.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gw)

	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		name, _ := filepath.Rel(dir, p)

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(p)
			if err != nil {
				return err
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		
		// STRICT METADATA CLAMPING FOR DETERMINISM
		hdr.ModTime = time.Unix(0, 0)
		hdr.AccessTime = time.Unix(0, 0)
		hdr.ChangeTime = time.Unix(0, 0)
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "root", "root"

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f2, _ := os.Open(p)
			io.Copy(tw, f2)
			f2.Close()
		}
		return nil
	})

	tw.Close()
	gw.Close()
	f.Close()
	return err
}

func addTarFile(tw *tar.Writer, name string, data []byte) {
	h := &tar.Header{
		Name:       name,
		Mode:       0o644,
		Size:       int64(len(data)),
		ModTime:    time.Unix(0, 0),
		AccessTime: time.Unix(0, 0),
		ChangeTime: time.Unix(0, 0),
		Typeflag:   tar.TypeReg,
		Uid:        0,
		Gid:        0,
		Uname:      "root",
		Gname:      "root",
	}
	tw.WriteHeader(h)
	tw.Write(data)
}
