package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	sst "github.com/swiss-software-tool/sst/internal/sst"
	hardwarepb "github.com/swiss-software-tool/sst/proto/yaml/hardware/v1"
)

var probeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Probe local hardware and write hardware.yaml",
	RunE:  runProbe,
}

func init() {
	rootCmd.AddCommand(probeCmd)
}

func runProbe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("probe")

	outPath := "hardware.yaml"
	if len(args) > 0 {
		outPath = args[0]
	}
	outPath, _ = filepath.Abs(outPath)

	devs, err := probeBlockDevices()
	if err != nil {
		return err
	}

	probe := &hardwarepb.HardwareProbe{
		ApiVersion: "sst.io/v1",
		Kind:       "HardwareProbe",
		Spec: &hardwarepb.HardwareSpec{
			BlockDevices: devs,
		},
	}

	yo := &sst.YamlObject{
		Path:   outPath,
		Schema: "sst.yaml.hardware.v1.HardwareProbe",
		Kind:   "HardwareProbe",
		Msg:    probe,
	}

	if diags := yo.Save(ctx, log); diags != nil {
		return diags
	}

	log.Info(ctx, "hardware probe written", "path", outPath)
	return nil
}

func probeBlockDevices() ([]*hardwarepb.BlockDevice, error) {
	cmd := exec.Command(
		"lsblk",
		"-dn",
		"-o", "NAME,TYPE,MODEL,SERIAL",
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %w", err)
	}

	var devices []*hardwarepb.BlockDevice

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, ln := range lines {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		if f[1] != "disk" {
			continue
		}

		dev := &hardwarepb.BlockDevice{
			Name:     f[0],
			HostPath: "/dev/" + f[0],
		}

		if len(f) > 2 {
			dev.Info = &hardwarepb.BlockDeviceInfo{
				Model: f[2],
			}
		}
		if len(f) > 3 {
			if dev.Info == nil {
				dev.Info = &hardwarepb.BlockDeviceInfo{}
			}
			dev.Info.Serial = f[3]
		}

		devices = append(devices, dev)
	}

	if len(devices) == 0 {
		return nil, fmt.Errorf("no block devices found")
	}

	return devices, nil
}
