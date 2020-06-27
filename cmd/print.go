package cmd

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"

	"github.com/spf13/cobra"
	rein "github.com/steviebps/rein/pkg"
)

var c rein.Chamber = rein.Chamber{Toggles: []rein.Toggle{}, Children: []rein.Chamber{}}

// printCmd represents the print command
var printCmd = &cobra.Command{
	Use:   "print",
	Short: "Print all Chambers",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		file, _ := cmd.Flags().GetString("file")
		pretty, _ := cmd.Flags().GetBool("pretty")

		jsonFile, err := os.Open(file)
		if err != nil {
			log.Fatal("Could not open configuration file...")
		}
		defer jsonFile.Close()

		byteValue, err := ioutil.ReadAll(jsonFile)
		if err != nil {
			log.Fatal(err)
		}

		if err := json.Unmarshal(byteValue, &c); err != nil {
			log.Fatal(err)
		}

		c.Print(os.Stdout, pretty)
	},
}

func init() {
	rootCmd.AddCommand(printCmd)

	printCmd.Flags().StringP("file", "f", "sample.json", "The file to read configuration from")
	printCmd.Flags().BoolP("pretty", "p", false, "Prints in pretty format")
}
