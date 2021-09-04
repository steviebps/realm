package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steviebps/rein/internal/logger"
	rein "github.com/steviebps/rein/pkg"
	"github.com/steviebps/rein/utils"
)

var chamberName string
var toStdout bool

var buildCmdError = logger.ErrorWithPrefix("Error running build command: ")

// buildCmd represents the build command
var buildCmd = &cobra.Command{
	Use:     "build",
	Short:   "Build chambers with inherited toggles",
	Long:    `Build command will take your chamber configs and compile them with their inherited values`,
	Example: "rein build -o /path/to/your/directory",
	Run: func(cmd *cobra.Command, args []string) {
		outputDir, _ := cmd.Flags().GetString("output-dir")
		forceCreateDir, _ := cmd.Flags().GetBool("force")
		chamberName, _ = cmd.Flags().GetString("chamber")
		toStdout, _ = cmd.Flags().GetBool("to-stdout")

		var fullPath string
		var err error

		if !toStdout {
			fullPath, err = getOutputDirectory(outputDir)
			if err != nil {
				buildCmdError(err.Error())
				os.Exit(1)
			}

			if _, err = os.Stat(fullPath); os.IsNotExist(err) {
				if forceCreateDir {
					os.Mkdir(fullPath, 0700)
				} else {
					buildCmdError(fmt.Sprintf("Directory %v does not exist", fullPath))
					logger.InfoString(fmt.Sprintf("\nTry running: \"rein build --output-dir %v --force\" to force create the directory", outputDir))
					os.Exit(1)
				}
			}
		}

		build(&globalChamber, fullPath, cmd)
		os.Exit(0)
	},
}

func getOutputDirectory(outputDir string) (string, error) {
	// defaults to working directory
	if outputDir == "" {
		return os.Getwd()
	}

	return filepath.Abs(outputDir)
}

func build(parent *rein.Chamber, fullPath string, cmd *cobra.Command) {

	parent.TraverseAndBuild(func(c *rein.Chamber) bool {

		searchingByName := chamberName != ""
		foundByName := chamberName == c.Name

		if foundByName || (!searchingByName && (c.IsBuildable || c.IsApp)) {

			if toStdout {
				if err := utils.WriteInterfaceWith(cmd.OutOrStdout(), c.Toggles, true); err != nil {
					buildCmdError(err.Error())
					os.Exit(1)
				}
			} else {
				fileName := fullPath + "/" + c.Name + ".json"
				file, err := os.OpenFile(fileName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					buildCmdError(err.Error())
					os.Exit(1)
				}

				if err := utils.WriteInterfaceWith(file, c.Toggles, true); err != nil {
					buildCmdError(err.Error())
					os.Exit(1)
				}

				fmt.Println(fileName)
			}

		}
		return foundByName
	})
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("output-dir", "o", "", "sets the output directory of the built files")
	buildCmd.Flags().BoolP("force", "f", false, "force create directory (used with output-dir)")
	buildCmd.Flags().StringP("chamber", "c", "", "builds the selected chamber only")
	buildCmd.Flags().Bool("to-stdout", false, "prints the built files to stdout (overrides output-dir flag)")
}
