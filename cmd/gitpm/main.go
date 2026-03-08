package main

import (
	"os"

	"github.com/swiss-software-tool/sst/internal/gitpm"
)

func main() {
	if err := gitpm.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
