package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	rein "github.com/steviebps/rein/pkg"
)

// openCmd represents the open command
var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open your chambers",
	Long:  `Open your chambers for viewing or editing`,
	Run: func(cmd *cobra.Command, args []string) {
		openChamberOptions(&c)
	},
}

var templates promptui.SelectTemplates = promptui.SelectTemplates{
	Label:    "{{ . }}?",
	Active:   "\U0001F579 {{ .Associated.Name | cyan }} ({{ len .Associated.Toggles | red }})",
	Inactive: "  {{ .Associated.Name | cyan }} ({{ len .Associated.Toggles  | red }})",
	Selected: "\U0001F579 {{ .Associated.Name | red | cyan }}",
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
	Selected: "\U0001F579 {{ .Name | red | cyan }}",
	Details: `
--------- Chamber ----------
{{ "Name:" | faint }}	{{ .Associated.Name }}
{{ "isApp:" | faint }}	{{ .Associated.App }}
{{ "isBuildable:" | faint }}	{{ .Associated.Buildable }}
{{ "# of toggles:" | faint }}	{{ len .Associated.Toggles }}
{{ "# of children:" | faint }}	{{ len .Associated.Children }}`,
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().StringP("name", "n", "", "Name of the chamber")
}

func nameValidation(name string) error {
	if name == "" {
		return errors.New("Invalid name!")
	}
	found := c.FindByName(name)

	if found == nil {
		return errors.New("Could not find chamber!")
	}

	return nil
}

func openNamePrompt() string {
	prompt := promptui.Prompt{
		Label:    "Chamber name",
		Validate: nameValidation,
	}

	name, err := prompt.Run()
	if err != nil {
		fmt.Printf("Prompt failed %v\n", err)
		os.Exit(1)
	}

	return name
}

func openChildrenSelect(chamber *rein.Chamber) {
	var options []openOption

	for _, child := range chamber.Children {
		option := openOption{
			Name:       child.Name,
			Associated: child,
			Action: func(c rein.Chamber) {
				openChamberOptions(&c)
			},
		}
		options = append(options, option)
	}

	selectPrompt := promptui.Select{
		Label:     "Select Chamber",
		Items:     options,
		Templates: &templates,
		HideHelp:  true,
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
			Associated: *chamber,
			Action:     func(rein.Chamber) {},
		},
	}

	if len(chamber.Children) > 0 {
		option := openOption{
			Name:       "Open children...",
			Associated: *chamber,
			Action: func(rein.Chamber) {
				openChildrenSelect(chamber)
			}}
		options = append(options, option)
	}

	selectPrompt := promptui.Select{
		Label:     "What next",
		Items:     options,
		Templates: &optionsTemplates,
		HideHelp:  true,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	options[i].Run()
}
