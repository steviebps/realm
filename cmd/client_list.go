package cmd

import (
	"github.com/spf13/cobra"
)

// clientList represents the client list command
var clientList = &cobra.Command{
	Use:          "list [path]",
	Short:        "list chambers",
	Long:         "list chambers at the specified path",
	SilenceUsage: true,
	Args:         chamberPathArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runChamberRequest(cmd, chamberRequest{verb: "list", method: "GET", path: args[0], query: "list=true", print: true})
	},
}

func init() {
	clientCmd.AddCommand(clientList)
}
