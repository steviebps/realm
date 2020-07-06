package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/steviebps/rein/utils"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	rein "github.com/steviebps/rein/pkg"
)

var templates promptui.SelectTemplates = promptui.SelectTemplates{
	Label:    "{{ . }}?",
	Active:   "\U0001F579 {{ .Associated.Name | cyan }} ({{ len .Associated.Toggles | red }})",
	Inactive: "  {{ .Associated.Name | cyan }} ({{ len .Associated.Toggles  | red }})",
	Details: `
--------- Chamber ----------
{{ "Name:" | faint }}	{{ .Associated.Name }}
{{ "isApp:" | faint }}	{{ .Associated.App }}
{{ "isBuildable:" | faint }}	{{ .Associated.Buildable }}
{{ "# of toggles:" | faint }}	{{ len .Associated.Toggles }}
{{ "# of children:" | faint }}	{{ len .Associated.Children }}`,
}

var optionsTemplates promptui.SelectTemplates = promptui.SelectTemplates{
	Label:    "{{ . }}?",
	Active:   "\U0001F579 {{ .Name | cyan }}",
	Inactive: "  {{ .Name | cyan }}",
	Details: `
--------- Chamber ----------
{{ "Name:" | faint }}	{{ .Associated.Name }}
{{ "isApp:" | faint }}	{{ .Associated.App }}
{{ "isBuildable:" | faint }}	{{ .Associated.Buildable }}
{{ "# of toggles:" | faint }}	{{ len .Associated.Toggles }}
{{ "# of children:" | faint }}	{{ len .Associated.Children }}`,
}

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
			Action: func(asssociated *rein.Chamber) {
				openChamberOptions(asssociated)
			},
		}
		options = append(options, option)
	}

	selectPrompt := promptui.Select{
		Label:        "Select Chamber",
		Items:        options,
		Templates:    &templates,
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
			Name:       "Edit",
			Associated: chamber,
			Action: func(asssociated *rein.Chamber) {
				editChamberOptions(asssociated, 0)
			},
		},
	}

	if len(chamber.Children) > 0 {
		option := openOption{
			Name:       "Open children...",
			Associated: chamber,
			Action: func(*rein.Chamber) {
				openChildrenSelect(chamber)
			}}
		options = append(options, option)
	}

	options = append(options, saveExit)
	options = append(options, exit)

	selectPrompt := promptui.Select{
		Label:        "What shall you do",
		Items:        options,
		Templates:    &optionsTemplates,
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
		Label:        "What value do you want to edit",
		Items:        options,
		Templates:    &optionsTemplates,
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
