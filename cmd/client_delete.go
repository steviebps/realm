package cmd

import (
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
)

// clientDelete represents the client delete command
var clientDelete = &cobra.Command{
	Use:          "delete [path]",
	Short:        "delete a chamber",
	Long:         "delete a chamber at the specified path",
	SilenceUsage: true,
	Args:         chamberPathArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := runChamberRequest(cmd, chamberRequest{verb: "delete", method: "DELETE", path: args[0]}); err != nil {
			return err
		}
		ctx := cmd.Context()
		logging.Ctx(ctx).InfoCtx(ctx).Msgf("successfully deleted %q", args[0])
		return nil
	},
}

func init() {
	clientCmd.AddCommand(clientDelete)
}
