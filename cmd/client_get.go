package cmd

import (
	"github.com/spf13/cobra"
)

// clientGet represents the client get command
var clientGet = &cobra.Command{
	Use:          "get [path]",
	Short:        "get a chamber",
	Long:         "get retrieves the chamber at the specified path",
	SilenceUsage: true,
	Args:         chamberPathArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runChamberRequest(cmd, chamberRequest{verb: "get", method: "GET", path: args[0], print: true})
	},
}

func init() {
	clientCmd.AddCommand(clientGet)
}
