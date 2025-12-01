package main

import (
    "github.com/spf13/cobra"
)

func main() {
    rootCmd := &cobra.Command{
        Use:   "mkimage",
        Short: "Create SST VM image directories from upstream sources",
        Long:  "mkimage --convert converts a qcow2 or URL into an SST image family layout.",
    }

    rootCmd.AddCommand(convertCmd)

    if err := rootCmd.Execute(); err != nil {
        panic(err)
    }
}
