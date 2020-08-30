package cmd

import (
	"fmt"
	"os"

	"github.com/steviebps/rein/options"
	"github.com/steviebps/rein/templates"

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

func openChildrenChambers(chamber *rein.Chamber) {
	opts := make([]options.OpenOption, 0)

	for _, child := range chamber.Children {
		option := options.New(child.Name, child, child, openChamberOptions)
		opts = append(opts, option)
	}

	selectPrompt := promptui.Select{
		Label:        "Select Chamber",
		Items:        opts,
		Templates:    &templates.ChamberTemplate,
		HideHelp:     true,
		HideSelected: true,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	opts[i].Run()
}

func openChamberOptions(chamber *rein.Chamber) {
	opts := make([]options.OpenOption, 0)

	editAction := func(asssociated *rein.Chamber) {
		editChamber(asssociated, 0)
	}
	edit := options.New(fmt.Sprintf("Edit \"%v\" chamber", chamber.Name), chamber, chamber, editAction)
	opts = append(opts, edit)

	if len(chamber.Children) > 0 {
		openChildren := options.New("Open children chambers", chamber, chamber, openChildrenChambers)
		opts = append(opts, openChildren)
	}

	exit := options.NewExit(chamber)
	saveAndExit := options.NewSaveAndExit(&globalChamber, chamber)
	opts = append(opts, exit, saveAndExit)

	selectPrompt := promptui.Select{
		Label:        "What shall you do",
		Items:        opts,
		Templates:    &templates.GenericWithChamberTemplate,
		HideHelp:     true,
		HideSelected: true,
	}

	i, _, err := selectPrompt.Run()
	if err != nil {
		fmt.Printf("Select failed %v\n", err)
		os.Exit(1)
	}
	opts[i].Run()
}

func editChamber(chamber *rein.Chamber, position int) {
	opts := make([]options.OpenOption, 0)

	toggleApp := func(associated *rein.Chamber) {
		associated.App = !associated.App
		editChamber(associated, 0)
	}

	toggleBuildable := func(associated *rein.Chamber) {
		associated.Buildable = !associated.Buildable
		editChamber(associated, 1)
	}

	selectToggleOption := func(associated *rein.Chamber) {
		selectToggle(associated, 0)
	}

	isApp := options.New("isApp", chamber, chamber, toggleApp)
	isBuildable := options.New("isBuildable", chamber, chamber, toggleBuildable)
	editToggles := options.New("Edit toggles", chamber, chamber, selectToggleOption)
	exit := options.NewExit(chamber)
	saveAndExit := options.NewSaveAndExit(&globalChamber, chamber)

	opts = append(opts, isApp, isBuildable, editToggles, exit, saveAndExit)

	selectPrompt := promptui.Select{
		Label:        "What would you like to edit",
		Items:        opts,
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
	opts[i].Run()
}

func selectToggle(chamber *rein.Chamber, position int) {
	opts := make([]options.OpenOption, 0)

	editToggle := func(toggle *rein.Toggle) options.SelectAction {
		return func(*rein.Chamber) {
			editToggleOptions(toggle)
		}
	}

	for _, child := range chamber.Toggles {
		option := options.New(child.Name, chamber, chamber, editToggle(child))
		opts = append(opts, option)
	}

	selectPrompt := promptui.Select{
		Label:        "Which toggle would you like to change",
		Items:        opts,
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
	opts[i].Run()
}

func editToggleOptions(toggle *rein.Toggle) {
	os.Exit(0)
}
