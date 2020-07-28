package templates

import "github.com/manifoldco/promptui"

var ChamberTemplate promptui.SelectTemplates = promptui.SelectTemplates{
	Label:    "{{ . }}?",
	Active:   "\U0001F579 {{ .Displayed.Name | cyan }} ({{ len .Displayed.Toggles | red }})",
	Inactive: "  {{ .Displayed.Name | cyan }} ({{ len .Displayed.Toggles  | red }})",
	Details: `
--------- Chamber ----------
{{ "Name:" | faint }}	{{ .Displayed.Name }}
{{ "isApp:" | faint }}	{{ .Displayed.App }}
{{ "isBuildable:" | faint }}	{{ .Displayed.Buildable }}
{{ "# of toggles:" | faint }}	{{ len .Displayed.Toggles }}
{{ "# of children:" | faint }}	{{ len .Displayed.Children }}`,
}

var GenericWithChamberTemplate promptui.SelectTemplates = promptui.SelectTemplates{
	Label:    "{{ . }}?",
	Active:   "\U0001F579 {{ .Name | cyan }}",
	Inactive: "  {{ .Name | cyan }}",
	Details: `
--------- Chamber ----------
{{ "Name:" | faint }}	{{ .Displayed.Name }}
{{ "isApp:" | faint }}	{{ .Displayed.App }}
{{ "isBuildable:" | faint }}	{{ .Displayed.Buildable }}
{{ "# of toggles:" | faint }}	{{ len .Displayed.Toggles }}
{{ "# of children:" | faint }}	{{ len .Displayed.Children }}`,
}
