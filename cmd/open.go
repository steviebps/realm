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

func openChildrenSelect(chamber *rein.Chamber) {
	var opts []options.OpenOption

	for _, child := range chamber.Children {
		option := options.NewOpen(child.Name, child, child, openChamberOptions)
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
	var opts []options.OpenOption

	editAction := func(asssociated *rein.Chamber) {
		editChamberOptions(asssociated, 0)
	}
	edit := options.NewOpen(fmt.Sprintf("Edit \"%v\" chamber", chamber.Name), chamber, chamber, editAction)
	opts = append(opts, edit)

	if len(chamber.Children) > 0 {
		openChildren := options.NewOpen("Open children chambers", chamber, chamber, openChildrenSelect)
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

func editChamberOptions(chamber *rein.Chamber, position int) {
	var opts []options.OpenOption
	toggleApp := func(associated *rein.Chamber) {
		associated.App = !associated.App
		editChamberOptions(associated, 0)
	}

	toggleBuildable := func(associated *rein.Chamber) {
		associated.Buildable = !associated.Buildable
		editChamberOptions(associated, 1)
	}

	isApp := options.NewOpen("isApp", chamber, chamber, toggleApp)
	isBuildable := options.NewOpen("isBuildable", chamber, chamber, toggleBuildable)
	exit := options.NewExit(chamber)
	saveAndExit := options.NewSaveAndExit(&globalChamber, chamber)

	opts = append(opts, isApp, isBuildable, exit, saveAndExit)

	selectPrompt := promptui.Select{
		Label:        "What value would you like to edit",
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
