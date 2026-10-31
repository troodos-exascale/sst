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

//go:build linux

package rtsst

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// This is a literal Go port of scripts/extract-vmlinux from the Linux tree.
// It uses the same external tools (file, readelf) and the same control flow.
//
// Instead of printing vmlinux to stdout, we write it to outPath.

const me = "extract-vmlinux-go"

// ExtractRawKernel extracts an uncompressed vmlinux from inPath and writes it
// to outPath. If no kernel can be extracted, it returns an error matching
// the script's "Cannot find vmlinux" behavior.
func ExtractRawKernel(inPath, outPath string) error {
	// Check invocation: single, non-empty file.
	st, err := os.Stat(inPath)
	if err != nil {
		return fmt.Errorf("stat kernel %s: %w", inPath, err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s: input kernel image is empty", me)
	}
	img := inPath

	// Prepare temp file (like mktemp /tmp/vmlinux-XXX).
	tmpFile, err := os.CreateTemp("", "vmlinux-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmp)

	// Read entire image into memory for offset scanning
	data, err := os.ReadFile(img)
	if err != nil {
		return fmt.Errorf("read %s: %w", img, err)
	}

	// check_vmlinux in the script. (Untouched original logic)
	checkVmlinux := func(candidate, method string, offset int64) (bool, error) {
		// file "$1" | grep -q 'Linux kernel.*boot executable'
		var fileOut bytes.Buffer
		cmdFile := exec.Command("file", candidate)
		cmdFile.Stdout = &fileOut
		cmdFile.Stderr = io.Discard
		if err := cmdFile.Run(); err == nil {
			s := fileOut.String()
			if strings.Contains(s, "Linux kernel") &&
				strings.Contains(s, "boot executable") {
				return copyOut(candidate, outPath, method, offset)
			}
		}

		// OR: readelf -h "$1" > /dev/null 2>&1
		cmdReadelf := exec.Command("readelf", "-h", candidate)
		cmdReadelf.Stdout = io.Discard
		cmdReadelf.Stderr = io.Discard
		if err := cmdReadelf.Run(); err == nil {
			return copyOut(candidate, outPath, method, offset)
		}

		return false, nil
	}

	// try_decompress using Go-native decompression streams
	tryGoDecompress := func(hdr []byte, decoName string, getDecoder func(io.Reader) (io.Reader, error)) (bool, error) {
		offset := 0
		for {
			pos := bytes.Index(data[offset:], hdr)
			if pos == -1 {
				break
			}
			abs := int64(offset + pos)

			// tail -c+$pos "$img" | $decompressor > $tmp
			reader := bytes.NewReader(data[abs:])
			decReader, err := getDecoder(reader)
			if err != nil {
				offset += pos + 1
				continue
			}

			out, err := os.Create(tmp)
			if err != nil {
				return false, fmt.Errorf("open temp for decompression: %w", err)
			}

			// Decompress straight to the temp file, ignoring stream errors (which indicate false magic bytes)
			_, _ = io.Copy(out, decReader)
			out.Close()

			if c, ok := decReader.(io.Closer); ok {
				_ = c.Close()
			} else if z, ok := decReader.(*zstd.Decoder); ok {
				z.Close()
			}

			ok, err := checkVmlinux(tmp, decoName, abs)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}

			offset += pos + 1
		}

		return false, nil
	}

	// try_decompress for tools with no native Go implementation (e.g. lzop)
	tryShellDecompress := func(hdr []byte, decoName string, argv ...string) (bool, error) {
		offset := 0
		for {
			pos := bytes.Index(data[offset:], hdr)
			if pos == -1 {
				break
			}
			abs := int64(offset + pos)

			// tail -c+$pos "$img" | $decompressor > $tmp
			reader := bytes.NewReader(data[abs:])
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Stdin = reader

			out, err := os.Create(tmp)
			if err != nil {
				return false, fmt.Errorf("open temp for decompression: %w", err)
			}
			cmd.Stdout = out
			cmd.Stderr = io.Discard

			_ = cmd.Run()
			out.Close()

			ok, err := checkVmlinux(tmp, decoName, abs)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}

			offset += pos + 1
		}

		return false, nil
	}

	// -------------------------------------------------------------------------
	// Mirror the script's exact sequence:
	// -------------------------------------------------------------------------

	// try_decompress '\037\213\010' xy    gunzip
	if ok, err := tryGoDecompress([]byte{0x1F, 0x8B, 0x08}, "gunzip", func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\3757zXZ\000' abcde unxz
	if ok, err := tryGoDecompress([]byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, "unxz", func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress 'BZh'          xy    bunzip2
	if ok, err := tryGoDecompress([]byte("BZh"), "bunzip2", func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\135\0\0\0'   xxx   unlzma
	if ok, err := tryGoDecompress([]byte{0x5D, 0x00, 0x00, 0x00}, "unlzma", func(r io.Reader) (io.Reader, error) { return lzma.NewReader(r) }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\211\114\132' xy    'lzop -d'
	if ok, err := tryShellDecompress([]byte{0x89, 0x4C, 0x5A}, "lzop -d", "lzop", "-d"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\002!L\030'   xxx   'lz4 -d'
	if ok, err := tryGoDecompress([]byte{0x02, 0x21, 0x4C, 0x18}, "lz4 -d", func(r io.Reader) (io.Reader, error) { return lz4.NewReader(r), nil }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '(\265/\375'   xxx   unzstd
	if ok, err := tryGoDecompress([]byte{0x28, 0xB5, 0x2F, 0xFD}, "unzstd", func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }); err != nil {
		return err
	} else if ok {
		return nil
	}

	// Finally check for uncompressed images or objects:
	// check_vmlinux "$img" cat 0
	if ok, err := checkVmlinux(img, "cat", 0); err != nil {
		return err
	} else if ok {
		return nil
	}

	// Bail out:
	return fmt.Errorf("%s: Cannot find vmlinux.", me)
}

// copyOut corresponds to the script's cat "$1" in check_vmlinux.
// It copies candidate → outPath and logs the extract-vmlinux message.
func copyOut(candidate, outPath, method string, offset int64) (bool, error) {
	in, err := os.Open(candidate)
	if err != nil {
		return false, fmt.Errorf("open candidate %s: %w", candidate, err)
	}
	defer in.Close()

	out, err := os.Create(outPath)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", outPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return false, fmt.Errorf("copy vmlinux to %s: %w", outPath, err)
	}

	fmt.Fprintf(os.Stderr, "%s: Extracted vmlinux using '%s' from offset %d\n",
		me, method, offset)
	return true, nil
}
