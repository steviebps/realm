package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/rein/utils"
)

// printCmd represents the print command
var printCmd = &cobra.Command{
	Use:   "print",
	Short: "Print all Chambers",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		pretty, _ := cmd.Flags().GetBool("pretty")
		output, _ := cmd.Flags().GetString("output")

		if output != "" {
			utils.WriteChamberToFile(output, globalChamber, pretty)
		} else {
			globalChamber.EncodeWith(cmd.OutOrStdout(), pretty)
		}
		os.Exit(0)
	},
}

func init() {
	rootCmd.AddCommand(printCmd)
	printCmd.Flags().BoolP("pretty", "p", false, "Prints in pretty format")
	printCmd.Flags().StringP("output", "o", "", "Sets the output file of the printed content")
}
