package cmd

import (
	"github.com/spf13/cobra"
	"github.com/steviebps/rein/internal/logger"
)

// Version the version of rein
var Version = "development"

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Get the current version of rein",
	Run: func(cmd *cobra.Command, args []string) {
		logger.InfoString(Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
