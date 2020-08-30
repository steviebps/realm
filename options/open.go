package options

import (
	rein "github.com/steviebps/rein/pkg"
)

// SelectAction is the callback to be executed when the option has been selected
type SelectAction func(*rein.Chamber)

// OpenOption is a CLI option for a select command
type OpenOption struct {
	Name       string
	Associated *rein.Chamber
	Displayed  *rein.Chamber
	Action     SelectAction
}

// New creates an "open" command option (OpenOption)
func New(name string, associated *rein.Chamber, displayed *rein.Chamber, action SelectAction) OpenOption {
	option := OpenOption{
		Name:       name,
		Displayed:  displayed,
		Associated: associated,
		Action:     action,
	}

	if option.Displayed == nil {
		option.Displayed = option.Associated
	}

	return option
}

// Run executes the Action with the Associated Chamber
func (option OpenOption) Run() {
	optionsList.PushFront(&option)
	option.Action(option.Associated)
}
