package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"golang.org/x/term"
)

// RunVMApp holds the application state and configuration
type RunVMApp struct {
	// --- Configuration Flags (matching Swift/crosvm) ---
	Name            string
	CPUCount        int
	MemorySize      uint64
	MemorySuffix    string
	DiskPaths       []string
	RwDiskPaths     []string
	CdromPaths      []string
	SharedDirs      []string
	Networks        []string
	Balloon         bool
	NestedVirt      bool   // <--- NEW: Enable nested virtualization
	Bootloader      string
	EfiVars         string
	Kernel          string
	Initrd          string
	Params          string
	EscapeSeq       string
	ShutdownTimeout float64
	Gui             bool

	// --- Runtime State ---
	ctx       context.Context
	cancel    context.CancelFunc
	log       *sst.Logx
	steps     *sst.StepsRunState
	vm        *VMWrapper
	ttyState  *term.State
	sigCh     chan os.Signal
}

func main() {
	app := &RunVMApp{
		log: sst.LogxRoot().Subsystem("runvm"),
	}

	cmd := &cobra.Command{
		Use:   "runvm",
		Short: "Native Go macOS Virtualization Runner",
		RunE: func(cmd *cobra.Command, args []string) error {
			app.ctx, app.cancel = context.WithCancel(context.Background())
			
			// Initialize the Step Machine
			app.steps = sst.StepsInit("runvm")
			
			app.steps.StepsAdd("validate_args", app.stepValidate)
			app.steps.StepsAdd("setup_tty", app.stepSetupTTY)
			app.steps.StepsAdd("configure_vm", app.stepConfigureVM)
			app.steps.StepsAdd("start_vm", app.stepStartVM)
			app.steps.StepsAdd("wait_loop", app.stepWaitLoop)
			
			defer app.cleanup()
			
			return sst.StepsRun(app.steps)
		},
	}

	// Register Flags
	f := cmd.Flags()
	f.StringVar(&app.Name, "name", "vm", "Instance name")
	f.IntVar(&app.CPUCount, "cpu-count", 1, "CPU count")
	f.Uint64Var(&app.MemorySize, "memory-size", 512, "Memory size")
	f.StringVar(&app.MemorySuffix, "memory-size-suffix", "MiB", "Memory unit (MiB, GiB)")
	f.StringSliceVar(&app.DiskPaths, "disk", []string{}, "Read-only disks")
	f.StringSliceVar(&app.RwDiskPaths, "rwdisk", []string{}, "Read-write disks")
	f.StringSliceVar(&app.CdromPaths, "cdrom", []string{}, "CD-ROMs")
	f.StringSliceVar(&app.SharedDirs, "shared-dir", []string{}, "Shared directories (path:tag:ro)")
	f.StringSliceVar(&app.Networks, "network", []string{"nat"}, "Network config (nat or mac@nat)")
	
	f.BoolVar(&app.Balloon, "balloon", true, "Enable memory ballooning")
	f.BoolVar(&app.NestedVirt, "nested-virt", false, "Enable nested virtualization (requires macOS 13+ Apple Silicon)") // <--- NEW FLAG

	f.StringVar(&app.Bootloader, "bootloader", "linux", "Bootloader (linux|efi)")
	f.StringVar(&app.EfiVars, "efi-vars", "", "Path to EFI variables")
	f.StringVar(&app.Kernel, "kernel", "", "Path to kernel")
	f.StringVar(&app.Initrd, "initrd", "", "Path to initrd")
	f.StringVar(&app.Params, "params", "", "Kernel command line")
	f.StringVar(&app.EscapeSeq, "escape-sequence", "q", "Terminal escape sequence")
	f.Float64Var(&app.ShutdownTimeout, "shutdown-timeout", 120.0, "Graceful shutdown timeout")
	f.BoolVar(&app.Gui, "gui", false, "Show GUI window")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- Step Implementations ---

func (a *RunVMApp) stepValidate(ctx *sst.StepsContext, s sst.StepState) error {
	if a.Bootloader == "linux" && a.Kernel == "" {
		return fmt.Errorf("kernel path required for linux bootloader")
	}

	// Calculate Memory Bytes
	mul := uint64(1024 * 1024) // Default MiB
	switch a.MemorySuffix {
	case "GiB": mul = 1024 * 1024 * 1024
	case "KB":  mul = 1000
	case "MB":  mul = 1000000
	case "GB":  mul = 1000000000
	}
	a.MemorySize = a.MemorySize * mul
	return nil
}

func (a *RunVMApp) stepSetupTTY(ctx *sst.StepsContext, s sst.StepState) error {
	// Set Raw Mode for interaction
	if term.IsTerminal(int(os.Stdin.Fd())) {
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			a.log.Warn(a.ctx, "Failed to set raw terminal", "error", err)
		} else {
			a.ttyState = state
		}
	}
	return nil
}

func (a *RunVMApp) stepConfigureVM(ctx *sst.StepsContext, s sst.StepState) error {
	vm, err := NewVMWrapper(a) // Logic in vz_darwin.go
	if err != nil {
		return err
	}
	a.vm = vm
	return nil
}

func (a *RunVMApp) stepStartVM(ctx *sst.StepsContext, s sst.StepState) error {
	if err := a.vm.Start(a.ctx); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}
	a.log.Info(a.ctx, "VM started")
	return nil
}

func (a *RunVMApp) stepWaitLoop(ctx *sst.StepsContext, s sst.StepState) error {
	a.sigCh = make(chan os.Signal, 1)
	signal.Notify(a.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Start CGO Sleep Observer
	stopSleepObs := StartSleepObserver(a.log, func(isSleep bool) {
		if isSleep {
			a.log.Info(a.ctx, "System sleeping, pausing VM...")
			if err := a.vm.Pause(); err != nil {
				a.log.Error(a.ctx, "Failed to pause VM", "error", err)
			}
		} else {
			a.log.Info(a.ctx, "System waking, resuming VM...")
			if err := a.vm.Resume(); err != nil {
				a.log.Error(a.ctx, "Failed to resume VM", "error", err)
			}
		}
	})
	defer stopSleepObs()

	// Wait loop
	select {
	case <-a.vm.Done():
		if err := a.vm.Err(); err != nil {
			return fmt.Errorf("VM error: %w", err)
		}
		a.log.Info(a.ctx, "VM exited cleanly")

	case sig := <-a.sigCh:
		a.log.Info(a.ctx, "Signal received", "signal", sig)
		
		a.vm.RequestStop()
		
		select {
		case <-a.vm.Done():
			a.log.Info(a.ctx, "VM stopped gracefully")
		case <-time.After(time.Duration(a.ShutdownTimeout) * time.Second):
			a.log.Warn(a.ctx, "Shutdown timed out, forcing stop")
			a.vm.Stop()
		}
	}

	return nil
}

func (a *RunVMApp) cleanup() {
	if a.ttyState != nil {
		term.Restore(int(os.Stdin.Fd()), a.ttyState)
	}
}
