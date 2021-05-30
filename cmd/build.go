package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
		var err error
		outputDir, _ := cmd.Flags().GetString("output-dir")
		forceCreateDir, _ := cmd.Flags().GetBool("force")
		chamberName, _ = cmd.Flags().GetString("chamber")
		toStdout, _ = cmd.Flags().GetBool("to-stdout")

		// defaults to working directory
		if outputDir == "" {
			outputDir, err = os.Getwd()
			if err != nil {
				buildCmdError(err.Error())
				os.Exit(1)
			}
		}

		outputDir, _ = filepath.Abs(outputDir)

		if _, err := os.Stat(outputDir); os.IsNotExist(err) {
			if forceCreateDir {
				os.Mkdir(outputDir, 0700)
			} else {
				buildCmdError(fmt.Sprintf("Directory %v does not exist", outputDir))
				logger.InfoString(fmt.Sprintf("\nTry running: \"rein build --output-dir %v --force\" to force create the directory", outputDir))
				os.Exit(1)
			}
		}

		var wg sync.WaitGroup
		build(&globalChamber, &wg, outputDir)
		wg.Wait()
		os.Exit(0)
	},
}

func build(parent *rein.Chamber, wg *sync.WaitGroup, outputDir string) {

	parent.TraverseAndBuild(func(c *rein.Chamber) bool {

		searchingByName := chamberName != ""
		foundByName := chamberName == c.Name

		if foundByName || (!searchingByName && (c.IsBuildable || c.IsApp)) {

			fileName := outputDir + "/" + c.Name + ".json"

			wg.Add(1)
			go func() {
				defer wg.Done()
				if toStdout {
					if err := utils.WriteInterfaceWith(os.Stdout, c.Toggles, true); err != nil {
						buildCmdError(err.Error())
						os.Exit(1)
					}
				} else {
					if err := utils.WriteInterfaceToFile(fileName, c.Toggles, true); err != nil {
						buildCmdError(err.Error())
						os.Exit(1)
					}
					fmt.Println(fileName)
				}
			}()
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
