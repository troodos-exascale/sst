package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/rtsst"
	"github.com/swiss-software-tool/sst/internal/sst"
	mkimagepb "github.com/swiss-software-tool/sst/proto/yaml/mkimage/v1"
)

var convertCmd = &cobra.Command{
	Use:   "convert <input_image> <name>",
	Short: "Convert a QCOW2 or raw disk image into an SST image directory",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Enforce root for deterministic loop device operations
		if os.Geteuid() != 0 {
			return fmt.Errorf("mkimg convert requires root privileges to mount loop devices. Please run with sudo")
		}

		inputImg := args[0]
		name := args[1]
		runtimeDir := flagRoot
		if runtimeDir == "" {
			runtimeDir = rtsst.RuntimeDir()
		}

		log := sst.LogxRoot().Subsystem("mkimage.convert")
		ctx := cmd.Context()

		outDir := filepath.Join(runtimeDir, "images", name)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		outRaw := filepath.Join(outDir, name+".raw")

		// 1. Probe Image Type
		isQcow, err := rtsst.IsQcow2(inputImg)
		if err != nil {
			return fmt.Errorf("failed to probe image type: %w", err)
		}

		// 2. Convert or Copy
		if isQcow {
			log.Info(ctx, "Converting QCOW2 to RAW", "input", inputImg, "output", outRaw)
			qemuCmd := exec.CommandContext(ctx, "qemu-img", "convert", "-f", "qcow2", "-O", "raw", inputImg, outRaw)
			if out, err := qemuCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("qemu-img convert failed: %s: %w", string(out), err)
			}
		} else {
			log.Info(ctx, "Copying RAW image natively", "input", inputImg, "output", outRaw)
			if err := rtsst.CopySparse(inputImg, outRaw); err != nil {
				return fmt.Errorf("failed to copy raw image: %w", err)
			}
		}

		buildID := time.Now().UTC().Format("20060102-150405")

		// 3. Prepare Image Manifest
		sb := &rtsst.ImageSB{
			Ctx:           ctx,
			Log:           log,
			SstRuntimeDir: runtimeDir,
			YamlObject: &mkimagepb.ImageYAML{
				ApiVersion: "sst.io/v1",
				Kind:       "Image",
				Provenance: &mkimagepb.Provenance{
					Name:        name,
					BuildId:     buildID,
					SourceUrl:   "local://" + filepath.Base(inputImg),
					RetrievedAt: time.Now().UTC().Format(time.RFC3339),
				},
				Storage: &mkimagepb.StoragePlan{
					RawImage: name + ".raw",
				},
				Boot: &mkimagepb.BootStructure{},
			},
		}

		if hash, err := rtsst.SHA256File(outRaw); err == nil {
			sb.YamlObject.Provenance.LocalSha256 = hash
		}

		// 4. Synchronous Partition Analysis & Extraction
		if err := rtsst.AnalyseImg(sb); err != nil {
			log.Warn(ctx, "AnalyseImg failed or no ext4/xfs partitions found", "error", err)
		} else if sb.YamlObject.Boot != nil && sb.YamlObject.Boot.BootfsDevice != "" {
			if err := rtsst.ExtractArtifacts(sb); err != nil {
				log.Warn(ctx, "Failed to extract kernel/initrd artifacts", "error", err)
			}
		}

		// 5. Commit YAML
		yo := &sst.YamlObject{
			Path:   filepath.Join(outDir, "image.yaml"),
			Schema: "sst.yaml.mkimage.v1.ImageYAML",
			Kind:   "Image",
			Msg:    sb.YamlObject,
		}

		if diags := yo.Save(ctx, log); diags != nil && diags.HasAny() {
			return diags
		}

		// 6. Cleanup & Catalog
		rtsst.QueuePurification(runtimeDir, []string{outRaw})
		rtsst.UpdateCatalog(ctx, log, "images", name, outDir, buildID)

		log.Info(ctx, "Conversion complete", "name", name, "dir", outDir)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(convertCmd)
}
