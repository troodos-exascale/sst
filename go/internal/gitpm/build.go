package bpmpkg

import (
	"fmt"
)

// BuildOptions mirrors CLI flags for constructing packages.
type BuildOptions struct {
	SourceType  string
	TargetType  string
	Name        string
	Version     string
	Prefix      string
	BuildDir    string
	Description string
}

// Build dispatches to the appropriate packager (deb, rpm, etc.).
func Build(opts BuildOptions) error {
	switch opts.TargetType {
	case "deb":
		o := &Options{
			InputType:   opts.SourceType,
			OutputType:  opts.TargetType,
			Name:        opts.Name,
			Version:     opts.Version,
			Chdir:       opts.BuildDir,
			Description: opts.Description,
		}
		out, err := BuildDeb(o)
		if err != nil {
			return fmt.Errorf("build failed: %v", err)
		}
		fmt.Printf("✅ built %s\n", out)
		return nil

	default:
		return fmt.Errorf("unsupported target type: %s", opts.TargetType)
	}
}
