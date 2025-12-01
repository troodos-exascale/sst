package image

import (
	"os"

	"cuelang.org/go/cue/cuecontext"
)

func ValidateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	ctx := cuecontext.New()
	schema := ctx.CompileBytes(ImageCue)
	manifest := ctx.CompileBytes(data)

	if err := schema.Unify(manifest).Err(); err != nil {
		return err
	}
	return nil
}
