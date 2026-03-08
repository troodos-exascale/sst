//go:build darwin
package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreFoundation
#import <CoreFoundation/CoreFoundation.h>
void runCocoaLoop() { CFRunLoopRun(); }
void stopCocoaLoop() { CFRunLoopStop(CFRunLoopGetMain()); }
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/sst"
)

func main() {
	if os.Getenv("SST_CROSVM_WORKER") == "1" {
		runWorker()
	} else {
		runSupervisor()
	}
}

func runSupervisor() {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "SST_CROSVM_WORKER=1")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	
	go func() {
		for sig := range sigCh {
			_ = cmd.Process.Signal(sig)
		}
	}()

	err := cmd.Wait()
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	os.Exit(0)
}

func init() {
	if os.Getenv("SST_CROSVM_WORKER") == "1" {
		runtime.LockOSThread()
	}
}

type CrosvmApp struct {
	Name            string
	CPUCount        int
	MemorySize      uint64
	MemorySuffix    string
	Blocks          []string
	RwDiskPaths     []string // Kept internal for vz_darwin
	DiskPaths       []string // Kept internal for vz_darwin
	SharedDirs      []string
	Networks        []string
	Kernel          string
	Initrd          string
	Params          string
	ShutdownTimeout float64
	Gui             bool

	NestedVirt      bool
	CdromPaths      []string
	Balloon         bool
	
	// Internal inferred state
	Bootloader      string
	EfiVars         string

	// Ignored upstream flags (prevent wrapper crash if runctl sends them)
	dummyBios       string

	ctx    context.Context
	cancel context.CancelFunc
	log    *sst.Logx
	steps  *sst.StepsRunState
	vm     *VMWrapper
	errCh  chan error
}

func runWorker() {
	app := &CrosvmApp{
		log:   sst.LogxRoot().Subsystem("crosvm-wrapper"),
		errCh: make(chan error, 1),
	}

	rootCmd := &cobra.Command{
		Use:           "crosvm",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "macOS VMM (crosvm-aligned flags)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Extract positional kernel argument if present
			if len(args) > 0 && app.Kernel == "" {
				app.Kernel = args[0]
			}
			
			app.ctx, app.cancel = context.WithCancel(context.Background())
			app.steps = sst.StepsInit("crosvm")
			app.steps.StepsAdd("validate", app.stepValidate)
			app.steps.StepsAdd("configure", app.stepConfigureVM)
			app.steps.StepsAdd("start", app.stepStartVM)
			app.steps.StepsAdd("wait", app.stepWaitLoop)

			go func() {
				if err := sst.StepsRun(app.steps); err != nil {
					app.errCh <- err
				}
				C.stopCocoaLoop()
			}()
			C.runCocoaLoop()

			select {
			case err := <-app.errCh:
				return err
			default:
				return nil
			}
		},
	}

	f := runCmd.Flags()
	f.StringVar(&app.Name, "name", "vm", "Instance name")
	f.IntVar(&app.CPUCount, "cpus", 1, "CPU count")
	f.Uint64Var(&app.MemorySize, "mem", 512, "Memory size (MiB)")
	
	f.StringSliceVar(&app.Blocks, "block", []string{}, "Block devices (path=...,ro=...)")
	f.StringSliceVar(&app.Networks, "net", []string{"nat"}, "Network config")
	f.StringSliceVar(&app.SharedDirs, "shared-dir", []string{}, "Shared directories")
	
	f.BoolVar(&app.NestedVirt, "nested-virt", false, "Enable nested virtualization")
	f.StringSliceVar(&app.CdromPaths, "cdrom", []string{}, "CD-ROMs")
	f.BoolVar(&app.Balloon, "balloon", true, "Enable memory ballooning")
	
	// FIX: Align EFI flags with upstream crosvm
	f.StringVar(&app.EfiVars, "pflash", "", "Path to EFI variables (upstream pflash equivalent)")
	f.StringVar(&app.dummyBios, "bios", "", "BIOS/UEFI payload (ignored on macOS, VZ provides its own)")

	f.StringVar(&app.Initrd, "initrd", "", "Initrd path")
	f.StringVar(&app.Params, "params", "", "Cmdline")
	f.BoolVar(&app.Gui, "gui", false, "Show GUI window")
	f.Float64Var(&app.ShutdownTimeout, "shutdown-timeout", 120.0, "Graceful timeout")
	
	rootCmd.AddCommand(runCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\r\n", err)
		os.Exit(1)
	}
}

func (a *CrosvmApp) stepValidate(ctx *sst.StepsContext, s sst.StepState) error {
	a.MemorySize = a.MemorySize * 1024 * 1024

	// FIX: Auto-detect bootloader type based on kernel arg presence.
	// This perfectly matches upstream behavior without needing a custom --bootloader flag.
	if a.Kernel == "" {
		a.Bootloader = "efi"
		if a.EfiVars == "" {
			a.EfiVars = "efi-vars.fd" // Safe default
		}
	} else {
		a.Bootloader = "linux"
	}

	// Translate standard crosvm --block flag back into legacy slices for macOS Virtualization
	for _, blk := range a.Blocks {
		parts := strings.Split(blk, ",")
		path := ""
		ro := false

		for _, p := range parts {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) == 2 {
				if kv[0] == "path" {
					path = kv[1]
				} else if kv[0] == "ro" {
					ro = (kv[1] == "true")
				}
			} else if len(kv) == 1 && path == "" {
				path = kv[0] // fallback for bare paths
			}
		}

		if path != "" {
			if ro {
				a.DiskPaths = append(a.DiskPaths, path)
			} else {
				a.RwDiskPaths = append(a.RwDiskPaths, path)
			}
		}
	}

	return nil
}

func (a *CrosvmApp) stepConfigureVM(ctx *sst.StepsContext, s sst.StepState) error {
	vm, err := NewVMWrapper(a)
	if err != nil { return err }
	a.vm = vm
	return nil
}

func (a *CrosvmApp) stepStartVM(ctx *sst.StepsContext, s sst.StepState) error {
	return a.vm.Start(a.ctx)
}

func (a *CrosvmApp) stepWaitLoop(ctx *sst.StepsContext, s sst.StepState) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Block indefinitely until the VM exits naturally, or we receive a signal
	select {
	case <-a.vm.Done():
		return a.vm.Err()
	case sig := <-sigCh:
		a.log.Info(a.ctx, "Received signal, requesting graceful shutdown", "signal", sig)
		_ = a.vm.RequestStop()

		// NOW we apply the 120-second graceful shutdown timeout
		select {
		case <-a.vm.Done():
			return a.vm.Err()
		case <-time.After(time.Duration(a.ShutdownTimeout) * time.Second):
			a.log.Warn(a.ctx, "Graceful shutdown timed out, forcing stop")
			_ = a.vm.Stop()
			return fmt.Errorf("shutdown timeout exceeded")
		}
	}
}
