package cmd

import (
	"fmt"
	"os"

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
		compile(&globalChamber)
		globalChamber.WriteWith(cmd.OutOrStdout(), true)
		os.Exit(0)
	},
}

func compile(parent *rein.Chamber) {
	for i := range parent.Children {
		built := parent.Children[i].InheritWith(parent.Toggles)
		parent.Children[i].Toggles = built
		fmt.Println(parent.Buildable || parent.App)
		if parent.Buildable || parent.App {
			file := "./" + parent.Name + ".json"
			utils.WriteChamberToFile(file, *parent, true)
		}
		compile(parent.Children[i])
	}
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
