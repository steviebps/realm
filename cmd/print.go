package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/rein/internal/logger"
	"github.com/steviebps/rein/utils"
)

var printCmdError = logger.ErrorWithPrefix("Error running print command: ")

// printCmd represents the print command
var printCmd = &cobra.Command{
	Use:    "print",
	Short:  "Print all Chambers",
	Long:   ``,
	PreRun: configPreRun,
	Run: func(cmd *cobra.Command, args []string) {
		pretty, _ := cmd.Flags().GetBool("pretty")
		output, _ := cmd.Flags().GetString("output")

		if output != "" {
			if err := utils.WriteChamberToFile(output, globalChamber, pretty); err != nil {
				printCmdError(err.Error())
				os.Exit(1)
			}
		} else {
			if err := globalChamber.EncodeWith(cmd.OutOrStdout(), pretty); err != nil {
				printCmdError(err.Error())
				os.Exit(1)
			}
		}

		os.Exit(0)
	},
}

func init() {
	rootCmd.AddCommand(printCmd)
	printCmd.Flags().BoolP("pretty", "p", false, "prints in pretty format")
	printCmd.Flags().StringP("output", "o", "", "sets the output file of the printed content")
}
