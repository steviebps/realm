package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
			f, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)

			if err != nil {
				fmt.Printf("Error opening file: %v\n", err)
				os.Exit(1)
			}
			c.Print(f, pretty)
			if err := f.Close(); err != nil {
				fmt.Printf("Error closing file: %v\n", err)
				os.Exit(1)
			}
		} else {
			c.Print(cmd.OutOrStdout(), pretty)
		}

	},
}

func init() {
	rootCmd.AddCommand(printCmd)
	printCmd.Flags().BoolP("pretty", "p", false, "Prints in pretty format")
	printCmd.Flags().StringP("output", "o", "", "Sets the output file of the printed content")
}
