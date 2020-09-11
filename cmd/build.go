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

// buildCmd represents the build command
var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build chambers with inherited toggles.",
	Long:  `Build command will take your chamber configs and compile them with their inherited values.`,
	Run: func(cmd *cobra.Command, args []string) {
		outputDir, _ := cmd.Flags().GetString("outputDir")
		var wg sync.WaitGroup
		compile(&globalChamber, outputDir, &wg)
		wg.Wait()
		os.Exit(0)
	},
}

func compile(parent *rein.Chamber, outputDir string, wg *sync.WaitGroup) {
	if parent.Buildable || parent.App {
		wg.Add(1)

		// defaults to current directory
		prefix := "./"
		if outputDir != "" {
			prefix, _ = filepath.Abs(outputDir)
		}

		if _, err := os.Stat(prefix); os.IsNotExist(err) {
			os.Mkdir(prefix, 0700)
		}

		file, _ := filepath.Abs(prefix + "/" + parent.Name + ".json")
		go func() {
			defer wg.Done()
			utils.WriteInterfaceToFile(file, parent.Toggles, true)
			fmt.Println(file)
		}()
	}

	for i := range parent.Children {
		built := parent.Children[i].InheritWith(parent.Toggles)
		parent.Children[i].Toggles = built
		compile(parent.Children[i], outputDir, wg)
	}
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("outputDir", "o", "", "Sets the output directory of the built files")
}
