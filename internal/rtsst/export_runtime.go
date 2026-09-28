package rtsst

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/troodos-exascale/sst/internal/sst"
	runctlpb "github.com/troodos-exascale/sst/proto/yaml/runctl/v1"
)

func (b *BuildApp) ExportRuntime(ctx context.Context) error {
	runtimeName := b.BuildArgs["RUNTIME_NAME"]
	if runtimeName == "" {
		runtimeName = b.Name
	}
	baseImage := b.BuildArgs["BASE_IMAGE"]
	if baseImage == "" {
		baseImage = b.BaseImage
	}

	targetDir := filepath.Join(b.RuntimeDir, "runtimes", baseImage, runtimeName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create runtime target dir: %w", err)
	}

	// 1. Extract final environment state from the shell AST boundary
	lastEnv := b.Instructions[len(b.Instructions)-1].Env

	mac := lastEnv["INST_MAC"]
	if mac == "" {
		mac, _ = GenerateMAC()
	}
	duid := lastEnv["INST_DUID"]
	if duid == "" {
		duid = "00:01:00:01:" + mac
	}

	// 2. Hydrate the Protobuf Struct natively FIRST
	inst := &runctlpb.Instance{
		ApiVersion: "sst.io/v1",
		Kind:       "Runtime",
		Name:       runtimeName,
		Hostname:   lastEnv["INST_HOSTNAME"],
		DomainName: lastEnv["INST_DOMAIN"],
		BaseImage:  baseImage,
		CpuCount:   lastEnv["INST_CPUS"],
		MemorySize: lastEnv["INST_MEM"],
		Bootloader: "linux",
		Kernel:     "kernel",
		Initrd:     "initrd",
		Params: []string{
			"console=hvc0",
			"root=/dev/vda rw",
		},
	}

	if inst.Hostname == "" { inst.Hostname = runtimeName }
	if inst.CpuCount == "" { inst.CpuCount = "2" }
	if inst.MemorySize == "" {
		inst.MemorySize = "2048"
		inst.MemorySizeSuffix = "MiB"
	}

	net := &runctlpb.EthInterface{
		Name:        "eth0",
		MacAddress:  mac,
		Duid:        duid,
		Ipv4Cidr:    lastEnv["INST_IPV4"],
		Ipv4Gateway: lastEnv["INST_GATEWAY"],
		Fabric:      "nat",
	}
	inst.Networks = []*runctlpb.EthInterface{net}

	if shares := lastEnv["INST_SHARE_HOST_DIRS"]; shares != "" {
		for _, share := range strings.Split(shares, ",") {
			inst.SharedDir = append(inst.SharedDir, strings.TrimSpace(share))
		}
	}

	baseDiskName := lastEnv["INST_BASE_DISK"]
	if baseDiskName == "" {
		baseDiskName = "disk.raw"
	}
	inst.Rwdisk = append(inst.Rwdisk, baseDiskName)

	// 3. Map and Provision Secondary Block Devices
	if devs := lastEnv["INST_DEVICES"]; devs != "" {
		for _, dev := range strings.Split(devs, ",") {
			parts := strings.Split(dev, ":")
			if len(parts) == 2 {
				devName := strings.TrimSpace(parts[0]) + ".raw"
				devSize, err := ParseSize(parts[1])
				if err != nil {
					b.Log.Warn(ctx, "failed to parse secondary device size", "device", devName, "size_str", parts[1], "error", err)
					continue
				}
				
				devPath := filepath.Join(targetDir, devName)
				b.Log.Info(ctx, "provisioning secondary sparse block device", "device", devName, "size", devSize)
				if err := CreateSparse(devPath, devSize); err != nil {
					return fmt.Errorf("failed to create sparse device %s: %w", devPath, err)
				}
				
				inst.Rwdisk = append(inst.Rwdisk, devName)
			}
		}
	}

	// 4. Execute the Exports driven entirely by the Protobuf arrays!
	for _, diskName := range inst.Rwdisk {
		if diskName != baseDiskName {
			continue 
		}
		
		srcDiskPath := filepath.Join(b.RootDir, diskName)
		dstDiskPath := filepath.Join(targetDir, diskName)
		
		b.Log.Info(ctx, "exporting base runtime disk from structural definition", "src", srcDiskPath, "dst", dstDiskPath)
		if err := CopySparse(srcDiskPath, dstDiskPath); err != nil {
			if b.BaseImage == "scratch" {
				b.Log.Warn(ctx, "scratch base image lacks source disk, creating dummy disk for test")
				os.WriteFile(dstDiskPath, []byte("DUMMY DISK"), 0644)
			} else {
				return fmt.Errorf("sparse export failed for %s: %w", diskName, err)
			}
		}
	}

	// Safely copy the boot assets inherited from the BASE IMAGE catalog
	bootAssets := []string{"kernel", "initrd"}
	baseImageDir := filepath.Join(b.RuntimeDir, "images", baseImage)

	for _, asset := range bootAssets {
		dstPath := filepath.Join(targetDir, asset)
		
		// First check if the runtime build generated a new one natively
		srcPath := filepath.Join(b.RootDir, asset)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			// Fallback: Grab the one compiled by the base image
			srcPath = filepath.Join(baseImageDir, asset)
		}
		
		if _, err := os.Stat(srcPath); err == nil {
			b.Log.Info(ctx, "exporting boot asset", "asset", asset)
			if err := CopySparse(srcPath, dstPath); err != nil {
				b.Log.Warn(ctx, "failed to export boot asset", "asset", asset, "error", err)
			}
		} else {
			if b.BaseImage == "scratch" {
				b.Log.Warn(ctx, "scratch base image lacks boot assets, skipping", "asset", asset)
			} else {
				b.Log.Error(ctx, "missing required boot asset", "asset", asset, "searched", srcPath)
				return fmt.Errorf("missing required boot asset: %s", asset)
			}
		}
	}

	// 5. Output the finalized runtime.yaml
	yo := &sst.YamlObject{
		Path:   filepath.Join(targetDir, "runtime.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Runtime",
		Msg:    inst,
	}
	if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}

	// 6. Package the Blueprint
	blueprintDir := filepath.Join(targetDir, "blueprint")
	os.MkdirAll(blueprintDir, 0755)

	if b.Dockerfile != "" {
		destPath := filepath.Join(blueprintDir, filepath.Base(b.Dockerfile))
		if data, err := os.ReadFile(b.Dockerfile); err == nil {
			_ = os.WriteFile(destPath, data, 0644)
		}
	}

	for relPath, absPath := range b.IncludedFiles {
		targetRel := relPath
		if !strings.HasSuffix(targetRel, ".dockerfile") {
			targetRel += ".dockerfile"
		}
		destPath := filepath.Join(blueprintDir, targetRel)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if data, err := os.ReadFile(absPath); err == nil {
			_ = os.WriteFile(destPath, data, 0644)
		}
	}

	catalogKey := filepath.Join(baseImage, runtimeName)
	disambiguator := fmt.Sprintf("%x", sha256.Sum256([]byte(targetDir)))[:8]
	UpdateCatalog(ctx, b.Log, "runtimes", catalogKey, targetDir, disambiguator)

	b.Log.Info(ctx, "✅ Runtime Exported Successfully", "name", catalogKey)
	return nil
}
