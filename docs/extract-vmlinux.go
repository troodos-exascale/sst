package rtsst

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// This is a literal Go port of scripts/extract-vmlinux from the Linux tree.
// It uses the same external tools (file, readelf, gunzip, unxz, bunzip2,
// unlzma, lzop -d, lz4 -d, unzstd) and the same control flow.
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

	// check_vmlinux in the script.
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

	// try_decompress in the script.
	tryDecompress := func(hdr []byte, decoName string, argv ...string) (bool, error) {
		data, err := os.ReadFile(img)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", img, err)
		}

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
				// script: exit 0 immediately on first success
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
	if ok, err := tryDecompress([]byte{0x1F, 0x8B, 0x08}, "gunzip", "gunzip"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\3757zXZ\000' abcde unxz
	if ok, err := tryDecompress([]byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, "unxz", "unxz"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress 'BZh'          xy    bunzip2
	if ok, err := tryDecompress([]byte("BZh"), "bunzip2", "bunzip2"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\135\0\0\0'   xxx   unlzma
	if ok, err := tryDecompress([]byte{0x5D, 0x00, 0x00, 0x00}, "unlzma", "unlzma"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\211\114\132' xy    'lzop -d'
	if ok, err := tryDecompress([]byte{0x89, 0x4C, 0x5A}, "lzop -d", "lzop", "-d"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '\002!L\030'   xxx   'lz4 -d'
	if ok, err := tryDecompress([]byte{0x02, 0x21, 0x4C, 0x18}, "lz4 -d", "lz4", "-d"); err != nil {
		return err
	} else if ok {
		return nil
	}

	// try_decompress '(\265/\375'   xxx   unzstd
	if ok, err := tryDecompress([]byte{0x28, 0xB5, 0x2F, 0xFD}, "unzstd", "unzstd"); err != nil {
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
