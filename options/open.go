package options

import (
	rein "github.com/steviebps/rein/pkg"
)

type SelectAction func(*rein.Chamber)

type OpenOption struct {
	Name       string
	Associated *rein.Chamber
	Displayed  *rein.Chamber
	Action     SelectAction
}

func New(name string, associated *rein.Chamber, displayed *rein.Chamber, action SelectAction) OpenOption {
	open := OpenOption{
		Name:       name,
		Displayed:  displayed,
		Associated: associated,
		Action:     action,
	}

	if open.Displayed == nil {
		open.Displayed = open.Associated
	}

	return open
}

func (option OpenOption) Run() {
	option.Action(option.Associated)
}
