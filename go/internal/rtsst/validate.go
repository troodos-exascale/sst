package rtsst

import (
	"fmt"
	"os"

	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
)

// ValidateFile loads YAML properly and unifies it with the embedded schema.
func ValidateFile(path string) error {

	// Read YAML
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	ctx := cuecontext.New()

	// Compile schema.cue (embedded in ImageCue)
	schema := ctx.CompileBytes(ImageCue)
	if err := schema.Err(); err != nil {
		return fmt.Errorf("schema compile error: %w", err)
	}

	// Load YAML properly via CUE loader
	instances := load.Instances([]string{path}, &load.Config{
		Overlay: map[string]load.Source{
			path: load.FromBytes(raw), // tell CUE this file is YAML
		},
	})

	if len(instances) == 0 {
		return fmt.Errorf("cue loader returned no instances for %s", path)
	}

	manifest := ctx.BuildInstance(instances[0])
	if err := manifest.Err(); err != nil {
		return fmt.Errorf("invalid YAML (cue parse error): %w", err)
	}

	// Unify with schema
	v := schema.Unify(manifest)
	if err := v.Err(); err != nil {
		return fmt.Errorf("image.yaml invalid: %w", err)
	}

	return nil
}
