package templates

import "github.com/manifoldco/promptui"

var ChamberTemplate promptui.SelectTemplates = promptui.SelectTemplates{
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

var GenericWithChamberTemplate promptui.SelectTemplates = promptui.SelectTemplates{
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
