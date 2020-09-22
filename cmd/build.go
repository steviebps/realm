package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"
	rein "github.com/steviebps/rein/pkg"
	"github.com/steviebps/rein/utils"
)

var outputDir string
var chamberName string
var toStdout bool

// buildCmd represents the build command
var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build chambers with inherited toggles.",
	Long:  `Build command will take your chamber configs and compile them with their inherited values.`,
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		outputDir, _ = cmd.Flags().GetString("output-dir")
		chamberName, _ = cmd.Flags().GetString("chamber")
		toStdout, _ = cmd.Flags().GetBool("to-stdout")

		// defaults to working directory
		if outputDir == "" {
			outputDir, err = os.Getwd()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

		}

		var wg sync.WaitGroup
		compile(&globalChamber, &wg)
		wg.Wait()
		os.Exit(0)
	},
}

func compile(parent *rein.Chamber, wg *sync.WaitGroup) {
	searchingByName := chamberName != ""
	foundByName := chamberName == parent.Name
	if (searchingByName && foundByName) || (!searchingByName && (parent.Buildable || parent.App)) {
		wg.Add(1)

		prefix, _ := filepath.Abs(outputDir)

		if _, err := os.Stat(prefix); os.IsNotExist(err) {
			os.Mkdir(prefix, 0700)
		}

		file, _ := filepath.Abs(prefix + "/" + parent.Name + ".json")
		go func() {
			defer wg.Done()
			if toStdout {
				utils.WriteInterfaceWith(os.Stdout, parent.Toggles, true)
			} else {
				utils.WriteInterfaceToFile(file, parent.Toggles, true)
				fmt.Println(file)
			}
		}()
	}

	for i := range parent.Children {
		built := parent.Children[i].InheritWith(parent.Toggles)
		parent.Children[i].Toggles = built
		compile(parent.Children[i], wg)
	}
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("output-dir", "o", "", "Sets the output directory of the built files.")
	buildCmd.Flags().StringP("chamber", "c", "", "Builds the selected chamber only.")
	buildCmd.Flags().Bool("to-stdout", false, "Prints the built files to stdout. (overrides output-dir flag)")
}
