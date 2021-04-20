package rein

import (
	"testing"
)

func TestFindByName(t *testing.T) {

	// setup
	bottom := &Chamber{Name: "BOTTOM"}
	middle := &Chamber{
		Name: "MIDDLE",
		Children: []*Chamber{
			bottom,
		},
	}
	middle2 := &Chamber{
		Name: "MIDDLE2",
	}
	top := &Chamber{
		Name: "TOP",
		Children: []*Chamber{
			middle,
			middle2,
		},
	}

	tests := []struct {
		input  string
		output *Chamber
	}{
		{"BOTTOM", bottom},
		{"MIDDLE", middle},
		{"MIDDLE2", middle2},
		{"TOP", top},
	}

	for _, test := range tests {
		got := top.FindByName(test.input)
		if got != test.output {
			t.Errorf("Got %v\nexpected: %q", got, test.output.Name)
		}
	}
}

func TestInheritWith(t *testing.T) {

	bottom := &Chamber{
		Name: "BOTTOM",
		Toggles: map[string]*Toggle{
			"toggle2": &Toggle{
				Name:       "toggle1",
				ToggleType: "boolean",
				Value:      false,
			},
		},
	}
	top := &Chamber{
		Name: "TOP",
		Toggles: map[string]*Toggle{
			"toggle1": &Toggle{
				Name:       "toggle1",
				ToggleType: "boolean",
				Value:      false,
			},
		},
	}
	middle := &Chamber{
		Name: "MIDDLE",
		Toggles: map[string]*Toggle{
			"toggle1": &Toggle{
				Name:       "toggle1",
				ToggleType: "boolean",
				Value:      false,
			},
		},
	}

	middle.InheritWith(top.Toggles)
	// should not inherit an already existent key
	bottom.InheritWith(middle.Toggles)

	if len(bottom.Toggles) != 2 {
		t.Errorf("%q did not inherit properly from %q", bottom.Name, top.Name)
	}
}
