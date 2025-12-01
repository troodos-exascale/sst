package main

import (
    "fmt"
    "os"

    "github.com/spf13/cobra"
)

func requireRoot() error {
    if os.Geteuid() != 0 {
        return fmt.Errorf("mkimage must be run as root")
    }
    return nil
}

func main() {

    rootCmd := &cobra.Command{
        Use:   "mkimage",
        Short: "Create SST VM image directories from upstream sources",
        Long:  "mkimage --convert converts a qcow2 or URL into an SST image family layout.",

        // (a) must run as root
        PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
            return requireRoot()
        },

        // (b) prevent Cobra from printing help or errors automatically
        SilenceUsage:  true,
        SilenceErrors: true,
    }

    rootCmd.AddCommand(convertCmd)

    // Manual single error handler
    if err := rootCmd.Execute(); err != nil {
        fmt.Fprintf(os.Stderr, "mkimage error: %v\n", err)
        os.Exit(1)
    }
}
