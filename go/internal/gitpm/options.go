package bpmpkg

import (
	"fmt"
	"runtime"
)

// Options defines all user-facing build parameters (FPM-compatible).
type Options struct {
	InputType    string   // -s, e.g. "dir"
	OutputType   string   // -t, e.g. "deb", "rpm"
	Name         string   // -n
	Version      string   // -v
	Arch         string   // --architecture
	Chdir        string   // -C
	Prefix       string   // --prefix
	License      string   // --license
	Maintainer   string   // --maintainer
	Description  string   // --description
	Depends      []string // --depends
	AfterInstall string   // --after-install (path)
}

// Validate ensures required options are present.
func (o *Options) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("missing required -n / --name")
	}
	if o.Version == "" {
		return fmt.Errorf("missing required -v / --version")
	}
	if o.OutputType == "" {
		return fmt.Errorf("missing required -t / --output-type")
	}
	if o.InputType == "" {
		return fmt.Errorf("missing required -s / --input-type")
	}
	if o.Chdir == "" {
		o.Chdir = "."
	}
	if o.Prefix == "" {
		o.Prefix = "/"
	}
	if o.Arch == "" {
		switch runtime.GOARCH {
		case "arm64":
			o.Arch = "arm64"
		case "amd64":
			o.Arch = "amd64"
		default:
			o.Arch = runtime.GOARCH
		}
	}
	return nil
}
