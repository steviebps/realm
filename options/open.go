package options

import (
	rein "github.com/steviebps/rein/pkg"
)

type selectAction func(*rein.Chamber)

type OpenOption struct {
	Name       string
	Associated *rein.Chamber
	Displayed  *rein.Chamber
	Action     selectAction
}

func NewOpen(name string, associated *rein.Chamber, displayed *rein.Chamber, action selectAction) OpenOption {
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
