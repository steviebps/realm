package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	utils "github.com/steviebps/rein/utils"
)

// printCmd represents the print command
var printCmd = &cobra.Command{
	Use:   "print",
	Short: "Print all Chambers",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		chamberFile := viper.GetString("chamber")
		pretty, _ := cmd.Flags().GetBool("pretty")

		if !utils.Exists(chamberFile) {
			fmt.Printf("Could not find chamber file: \"%s\"\n", chamberFile)
			os.Exit(1)
		}

		jsonFile, err := os.Open(chamberFile)
		if err != nil {
			fmt.Printf("Could not open chamber file: %s\n", chamberFile)
			os.Exit(1)
		}
		defer jsonFile.Close()

		byteValue, err := ioutil.ReadAll(jsonFile)
		if err != nil {
			fmt.Printf("Error reading chamber file: %s\n", err)
			os.Exit(1)
		}

		if err := json.Unmarshal(byteValue, &c); err != nil {
			fmt.Printf("Error reading JSON: %s\n", err)
			os.Exit(1)
		}

		c.Print(os.Stdout, pretty)
	},
}

func init() {
	rootCmd.AddCommand(printCmd)
	printCmd.Flags().BoolP("pretty", "p", false, "Prints in pretty format")
}
