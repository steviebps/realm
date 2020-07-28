package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	rein "github.com/steviebps/rein/pkg"
	"github.com/steviebps/rein/utils"
)

// buildCmd represents the build command
var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build chambers with inherited toggles.",
	Long:  `TODO`,
	Run: func(cmd *cobra.Command, args []string) {
		outputDir, _ := cmd.Flags().GetString("outputDir")
		compile(&globalChamber, outputDir)
		os.Exit(0)
	},
}

func compile(parent *rein.Chamber, outputDir string) {
	if parent.Buildable || parent.App {
		prefix := "./"
		if outputDir != "" {
			prefix = filepath.Dir(outputDir + "/")
		}

		file := prefix + "/" + parent.Name + ".json"
		utils.WriteInterfaceToFile(file, parent.Toggles, true)
	}

	for i := range parent.Children {
		built := parent.Children[i].InheritWith(parent.Toggles)
		parent.Children[i].Toggles = built

		compile(parent.Children[i], outputDir)
	}
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("outputDir", "o", "", "Sets the output directory of the built files")
}
