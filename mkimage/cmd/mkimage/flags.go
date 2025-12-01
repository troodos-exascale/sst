package main

func init() {
    convertCmd.Flags().StringVar(&flagRoot, "root", "",
        "Image root directory (default: ~/.runtimes/images)")

    convertCmd.Flags().BoolVar(&flagForce, "force", false,
        "Overwrite existing image directory if it exists")

    convertCmd.Flags().BoolVar(&flagVerbose, "verbose", false,
        "Verbose logging")
}
