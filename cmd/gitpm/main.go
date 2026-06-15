package main

import (
	"os"

	"github.com/troodos-exascale/sst/internal/gitpm"
)

func main() {
	if err := gitpm.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
