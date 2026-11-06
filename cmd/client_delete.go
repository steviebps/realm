package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
)

// clientDelete represents the client delete command
var clientDelete = &cobra.Command{
	Use:   "delete [path]",
	Short: "delete a chamber",
	Long: `delete the chamber at the specified path.

Chambers below the path are kept unless --recursive is given, which deletes
the chamber and every chamber below it.`,
	SilenceUsage: true,
	Args:         chamberPathArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		logger := logging.Ctx(ctx)
		path := args[0]

		recursive, _ := cmd.Flags().GetBool("recursive")
		if recursive {
			var result struct {
				Deleted []string `json:"deleted"`
			}
			err := runChamberRequest(cmd, chamberRequest{
				verb: "delete", method: "DELETE", path: path, query: "recursive=true",
				handle: func(data json.RawMessage) error { return json.Unmarshal(data, &result) },
			})
			if err != nil {
				return err
			}
			for _, p := range result.Deleted {
				fmt.Fprintln(cmd.OutOrStdout(), p)
			}
			logger.InfoCtx(ctx).Msgf("successfully deleted %d chamber(s) at and below %q", len(result.Deleted), path)
			return nil
		}

		if err := runChamberRequest(cmd, chamberRequest{verb: "delete", method: "DELETE", path: path}); err != nil {
			return err
		}
		logger.InfoCtx(ctx).Msgf("successfully deleted %q", path)

		// the chambers below remain, and keep the path visible as a folder
		if children := listChildren(cmd, path); len(children) > 0 {
			logger.WarnCtx(ctx).Msgf("%d chamber(s) remain below %q (%s); use --recursive to delete them too", len(children), path, strings.Join(children, ", "))
		}
		return nil
	},
}

// listChildren returns the names of the chambers directly below path, or nil
// if they cannot be listed.
func listChildren(cmd *cobra.Command, path string) []string {
	var names []string
	err := runChamberRequest(cmd, chamberRequest{
		verb: "list", method: "GET", path: path, query: "list=true",
		handle: func(data json.RawMessage) error { return json.Unmarshal(data, &names) },
	})
	if err != nil {
		return nil
	}
	// the file backend lists a chamber's own entry as "."
	return slices.DeleteFunc(names, func(n string) bool { return n == "." })
}

func init() {
	clientDelete.Flags().BoolP("recursive", "r", false, "also delete every chamber below the path")
	clientCmd.AddCommand(clientDelete)
}
