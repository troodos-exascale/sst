/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreFoundation

#import <CoreFoundation/CoreFoundation.h>

void runCocoaLoop() {
    CFRunLoopRun();
}

void stopCocoaLoop() {
    CFRunLoopStop(CFRunLoopGetMain());
}
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"golang.org/x/term"
)

// Main entry point decides role based on ENV var
func main() {
	if os.Getenv("SST_CROSVM_WORKER") == "1" {
		runWorker()
	} else {
		runSupervisor()
	}
}

// -----------------------------------------------------------------------------
// SUPERVISOR: Manages TTY and Signals. Survives child crashes.
// -----------------------------------------------------------------------------
func runSupervisor() {
	// 1. Setup TTY protection immediately
	fd := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(fd) {
		s, err := term.MakeRaw(fd)
		if err == nil {
			oldState = s
			// This defer guarantees cleanup even if child is kill -9'd
			defer term.Restore(fd, oldState)
		}
	}

	// 2. Prepare Child Command (Self re-execution)
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding executable: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "SST_CROSVM_WORKER=1")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 3. Start Child
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start worker process: %v\n", err)
		os.Exit(1)
	}

	// 4. Signal Forwarding
	// We forward INT/TERM to child. We ignore them in parent so parent waits for child.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	
	go func() {
		for sig := range sigCh {
			// Forward signal to child process group
			_ = cmd.Process.Signal(sig)
		}
	}()

	// 5. Wait for child
	err = cmd.Wait()
	
	// Exit with child's status code
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// -----------------------------------------------------------------------------
// WORKER: Runs the actual VM logic + Cocoa Loop
// -----------------------------------------------------------------------------

// Ensure worker runs on the main thread for Cocoa
func init() {
	if os.Getenv("SST_CROSVM_WORKER") == "1" {
		runtime.LockOSThread()
	}
}

// CrosvmApp holds application state (Worker only)
type CrosvmApp struct {
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
	NestedVirt      bool
	Bootloader      string
	EfiVars         string
	Kernel          string
	Initrd          string
	Params          string
	EscapeSeq       string
	ShutdownTimeout float64
	Gui             bool

	ctx    context.Context
	cancel context.CancelFunc
	log    *sst.Logx
	steps  *sst.StepsRunState
	vm     *VMWrapper
	sigCh  chan os.Signal
	errCh  chan error
}

func runWorker() {
	app := &CrosvmApp{
		log:   sst.LogxRoot().Subsystem("crosvm"),
		errCh: make(chan error, 1),
	}

	cmd := &cobra.Command{
		Use:   "crosvm",
		Short: "Native Go macOS Virtualization Runner",
		Run: func(cmd *cobra.Command, args []string) {
			app.ctx, app.cancel = context.WithCancel(context.Background())

			app.steps = sst.StepsInit("crosvm")
			// No TTY setup needed in worker; parent handled it.
			app.steps.StepsAdd("validate_args", app.stepValidate)
			app.steps.StepsAdd("configure_vm", app.stepConfigureVM)
			app.steps.StepsAdd("start_vm", app.stepStartVM)
			app.steps.StepsAdd("wait_loop", app.stepWaitLoop)

			// Run logic in goroutine
			go func() {
				err := sst.StepsRun(app.steps)
				if err != nil {
					app.log.WithError(err).Error(app.ctx, "crosvm failed")
					app.errCh <- err
				} else {
					app.errCh <- nil
				}
				C.stopCocoaLoop()
			}()

			// Block Main Thread on Cocoa RunLoop
			C.runCocoaLoop()

			if err := <-app.errCh; err != nil {
				os.Exit(1)
			}
			os.Exit(0)
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
	f.BoolVar(&app.NestedVirt, "nested-virt", false, "Enable nested virtualization")
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

func (a *CrosvmApp) stepValidate(ctx *sst.StepsContext, s sst.StepState) error {
	if a.Bootloader == "linux" && a.Kernel == "" {
		return fmt.Errorf("kernel path required for linux bootloader")
	}
	mul := uint64(1024 * 1024)
	switch a.MemorySuffix {
	case "GiB": mul = 1024 * 1024 * 1024
	case "KB":  mul = 1000
	case "MB":  mul = 1000000
	case "GB":  mul = 1000000000
	}
	a.MemorySize = a.MemorySize * mul
	return nil
}

func (a *CrosvmApp) stepConfigureVM(ctx *sst.StepsContext, s sst.StepState) error {
	vm, err := NewVMWrapper(a)
	if err != nil {
		return err
	}
	a.vm = vm
	return nil
}

func (a *CrosvmApp) stepStartVM(ctx *sst.StepsContext, s sst.StepState) error {
	if err := a.vm.Start(a.ctx); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}
	a.log.Info(a.ctx, "VM started")
	return nil
}

func (a *CrosvmApp) stepWaitLoop(ctx *sst.StepsContext, s sst.StepState) error {
	a.sigCh = make(chan os.Signal, 1)
	signal.Notify(a.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// stopSleepObs := StartSleepObserver(a.log, func(isSleep bool) {
	// 	if isSleep {
	// 		fmt.Fprintf(os.Stderr, "\n\n>>> SYSTEM SLEEP DETECTED - PAUSING VM <<<\n\n")
	// 		a.log.Info(a.ctx, "System sleeping, pausing VM...")
	// 		if err := a.vm.Pause(); err != nil {
	// 			a.log.Error(a.ctx, "Failed to pause VM", "error", err)
	// 		}
	// 	} else {
	// 		fmt.Fprintf(os.Stderr, "\n\n>>> SYSTEM WAKE DETECTED - RESUMING VM <<<\n\n")
	// 		a.log.Info(a.ctx, "System waking, resuming VM...")
	// 		if err := a.vm.Resume(); err != nil {
	// 			a.log.Error(a.ctx, "Failed to resume VM", "error", err)
	// 		}
	// 	}
	// })
	// defer stopSleepObs()

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
