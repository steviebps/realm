package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/steviebps/rein/templates"
	"github.com/steviebps/rein/utils"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	rein "github.com/steviebps/rein/pkg"
)

// openCmd represents the open command
var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open your chambers",
	Long:  `Open your chambers for viewing or editing`,
	Run: func(cmd *cobra.Command, args []string) {
		name, _ := cmd.Flags().GetString("name")
		openWith := &globalChamber

		if name != "" {
			if found := globalChamber.FindByName(name); found != nil {
				openWith = found
			}
		}

		openChamberOptions(openWith)
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().StringP("name", "n", "", "Name of the chamber")
}

var exit openOption = openOption{
	Name:       "Exit without saving",
	Associated: &globalChamber,
	Action: func(*rein.Chamber) {
		os.Exit(0)
	},
}

var saveExit openOption = openOption{
	Name:       "Save & Exit",
	Associated: &globalChamber,
	Action: func(asssociated *rein.Chamber) {
		chamberFile := viper.GetString("chamber")
		utils.SaveAndExit(chamberFile, *asssociated)
	},
}

var exitOptions []openOption = []openOption{exit, saveExit}

func nameValidation(name string) error {
	if name == "" {
		return errors.New("Invalid name!")
	}
	found := globalChamber.FindByName(name)

	if found == nil {
		return errors.New("Could not find chamber!")
	}

	return nil
}

func openChildrenSelect(chamber *rein.Chamber) {
	var options []openOption

	for _, child := range chamber.Children {
		option := openOption{
			Name:       child.Name,
			Associated: child,
			Action: openChamberOptions,
		}
		options = append(options, option)
	}

	selectPrompt := promptui.Select{
		Label:        "Select Chamber",
		Items:        options,
		Templates:    &templates.ChamberTemplate,
		HideHelp:     true,
		HideSelected: true,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	options[i].Run()
}

func openChamberOptions(chamber *rein.Chamber) {
	options := []openOption{
		{
			Name:       fmt.Sprintf("Edit \"%v\" chamber", chamber.Name),
			Associated: chamber,
			Action: func(asssociated *rein.Chamber) {
				editChamberOptions(asssociated, 0)
			},
		},
	}

	if len(chamber.Children) > 0 {
		option := openOption{
			Name:       "Open child chambers",
			Associated: chamber,
			Action:     openChildrenSelect,
		}
		options = append(options, option)
	}

	options = append(options, exitOptions...)

	selectPrompt := promptui.Select{
		Label:        "What shall you do",
		Items:        options,
		Templates:    &templates.GenericWithChamberTemplate,
		HideHelp:     true,
		HideSelected: true,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	options[i].Run()
}

func editChamberOptions(chamber *rein.Chamber, position int) {
	options := []openOption{
		{
			Name:       "isApp",
			Associated: chamber,
			Action: func(associated *rein.Chamber) {
				associated.App = !associated.App
				editChamberOptions(associated, 0)
			},
		},
		{
			Name:       "isBuildable",
			Associated: chamber,
			Action: func(associated *rein.Chamber) {
				associated.Buildable = !associated.Buildable
				editChamberOptions(associated, 1)
			},
		},
	}

	options = append(options, saveExit)
	options = append(options, exit)

	selectPrompt := promptui.Select{
		Label:        "What value would you like to edit",
		Items:        options,
		Templates:    &templates.GenericWithChamberTemplate,
		HideHelp:     true,
		HideSelected: true,
		CursorPos:    position,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	options[i].Run()
}
