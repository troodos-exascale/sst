package rtsst

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// Instruction represents a parsed line or block from the Dockerfile
type Instruction struct {
	Type string
	Args string
}

// BuildApp is the superblock for the mkimage build process
type BuildApp struct {
	Log          *sst.Logx
	Files        sst.Files
	Dockerfile   string
	BaseImage    string
	VMOUT        string
	Instructions []Instruction

	// Paths for reproducible staging
	RootDir     string
	OCIStaging  string
	ImageOutput string
}

func NewBuilder(log *sst.Logx, files sst.Files, dockerfile string) *BuildApp {
	return &BuildApp{
		Log:        log.Subsystem("rtsst-builder"),
		Files:      files,
		Dockerfile: dockerfile,
	}
}

// Parse implements the line-by-line scanner for the YAML-hybrid Dockerfile
func (b *BuildApp) Parse(ctx context.Context) error {
	f, err := os.Open(b.Dockerfile)
	if err != nil {
		return fmt.Errorf("failed to open Dockerfile: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var currentBlock []string
	var currentType string
	isMultiline := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if isMultiline {
			// Check if we are still inside the indented block
			if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				b.Instructions = append(b.Instructions, Instruction{
					Type: currentType,
					Args: strings.Join(currentBlock, "\n"),
				})
				currentBlock = nil
				isMultiline = false
			} else {
				blockLine := strings.TrimPrefix(line, "    ")
				blockLine = strings.TrimPrefix(blockLine, "\t")
				currentBlock = append(currentBlock, blockLine)
				continue
			}
		}

		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "FROM "):
			b.BaseImage = strings.TrimPrefix(trimmed, "FROM ")
		case strings.HasPrefix(upper, "VMOUT: "):
			b.VMOUT = strings.TrimPrefix(trimmed, "VMOUT: ")
		case strings.HasSuffix(trimmed, ": |"):
			currentType = strings.TrimSuffix(trimmed, ": |")
			isMultiline = true
		default:
			parts := strings.SplitN(trimmed, " ", 2)
			if len(parts) == 2 {
				b.Instructions = append(b.Instructions, Instruction{
					Type: parts[0],
					Args: parts[1],
				})
			}
		}
	}

	if isMultiline && len(currentBlock) > 0 {
		b.Instructions = append(b.Instructions, Instruction{
			Type: currentType,
			Args: strings.Join(currentBlock, "\n"),
		})
	}

	return scanner.Err()
}
