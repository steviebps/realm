package cmd

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"

	"github.com/spf13/cobra"
	rein "github.com/steviebps/rein/pkg"
)

// helloCmd represents the hello command
var printCmd = &cobra.Command{
	Use:   "print",
	Short: "Print all Chambers",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		// Open our jsonFile
		jsonFile, err := os.Open("sample.json")
		if err != nil {
			log.Fatal(err)
		}
		defer jsonFile.Close()

		byteValue, err := ioutil.ReadAll(jsonFile)
		if err != nil {
			log.Fatal(err)
		}

		var c rein.Chamber
		if err := json.Unmarshal(byteValue, &c); err != nil {
			log.Fatal(err)
		}

		enc := json.NewEncoder(os.Stdout)
		if pretty, err := cmd.Flags().GetBool("pretty"); pretty && err == nil {
			enc.SetIndent("", "\t")
		}

		if err := enc.Encode(c); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(printCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// helloCmd.PersistentFlags().String("foo", "", "A help for foo")

	printCmd.Flags().BoolP("pretty", "p", false, "Prints in pretty format")
}
