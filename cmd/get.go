package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/internal/logger"
	realm "github.com/steviebps/realm/pkg"
)

var getCmdError = logger.ErrorWithPrefix("error running get command: ")

// getCmd represents the get command
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a value of a toggle",
	Long:  "Retrieves and prints the value of the specified toggle within the specified chamber",
	Run: func(cmd *cobra.Command, args []string) {
		var value interface{}
		version, _ := cmd.Flags().GetString("app-version")
		toggle, _ := cmd.Flags().GetString("toggle")
		chamberName, _ = cmd.Flags().GetString("chamber")

		globalChamber.TraverseAndBuild(func(c realm.Chamber) bool {
			if c.Name == chamberName {
				value = c.GetToggleValue(toggle, version)
			}

			return value != nil
		})

		if value == nil {
			getCmdError(fmt.Sprintf("could not find toggle value %q inside chamber %q", toggle, chamberName))
			os.Exit(1)
		}

		fmt.Println(value)
	},
}

func init() {
	rootCmd.AddCommand(getCmd)

	getCmd.Flags().StringP("chamber", "c", "", "chamber to retrieve toggle from")
	getCmd.Flags().StringP("toggle", "t", "", "toggle name to retrieve")

	getCmd.MarkFlagRequired("toggle")
	getCmd.MarkFlagRequired("chamber")
}
