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
	top := &Chamber{
		Name: "TOP",
		Children: []*Chamber{
			middle,
		},
	}

	tests := []struct {
		input  string
		output *Chamber
	}{
		{"BOTTOM", bottom},
		{"MIDDLE", middle},
		{"TOP", top},
	}

	for _, test := range tests {
		got := top.FindByName(test.input)
		if got != test.output {
			t.Errorf("Got %v\nexpected: %q", got, test.output.Name)
		}
	}
}
