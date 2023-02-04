package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

// printCmd represents the print command
var printCmd = &cobra.Command{
	Use:   "print",
	Short: "Print all chambers",
	Long:  "Print all chambers as they exist without inheritence",
	Run: func(cmd *cobra.Command, args []string) {
		realmCore := cmd.Context().Value("core").(*realm.Realm)
		pretty, _ := cmd.Flags().GetBool("pretty")
		output, _ := cmd.Flags().GetString("output")

		var w io.Writer = cmd.OutOrStdout()
		var err error

		if output != "" {
			w, err = os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				realmCore.Logger().Info(err.Error())
				os.Exit(1)
			}
		}

		if err = utils.WriteInterfaceWith(w, globalChamber, pretty); err != nil {
			realmCore.Logger().Info(err.Error())
			os.Exit(1)
		}

		os.Exit(0)
	},
}

func init() {
	rootCmd.AddCommand(printCmd)
	printCmd.Flags().BoolP("pretty", "p", false, "prints in pretty format")
	printCmd.Flags().StringP("output", "o", "", "sets the output file of the printed content")
}
