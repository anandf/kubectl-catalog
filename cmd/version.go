package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// These variables are set via -ldflags at build time.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and build information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("kubectl-catalog version %s\n", version)
		fmt.Printf("  git commit: %s\n", gitCommit)
		fmt.Printf("  build date: %s\n", buildDate)

		if info, ok := debug.ReadBuildInfo(); ok {
			fmt.Printf("  go version: %s\n", info.GoVersion)
		}
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
