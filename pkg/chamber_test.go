package realm

import (
	"sync"
	"testing"
)

func TestInheritWith(t *testing.T) {

	bottom := &Chamber{
		Name: "BOTTOM",
		Toggles: map[string]*OverrideableToggle{
			"toggle2": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}
	middle := &Chamber{
		Name: "MIDDLE",
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: true,
				},
			},
		},
	}
	top := &Chamber{
		Name: "TOP",
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}

	middle.InheritWith(top.Toggles)
	bottom.InheritWith(middle.Toggles)

	if len(middle.Toggles) != 1 {
		t.Errorf("%q did not inherit properly from %q", middle.Name, top.Name)
	}

	// should not inherit top value as is
	if middle.Toggles["toggle1"].Value == top.Toggles["toggle1"].Value {
		t.Errorf("%q did not inherit properly from %q: value of toggle1 is: %v", middle.Name, top.Name, middle.Toggles["toggle1"].Value)
	}

	if len(bottom.Toggles) != 2 {
		t.Errorf("%q did not inherit properly from %q", bottom.Name, middle.Name)
	}

	// should inherit middle value as is
	if bottom.Toggles["toggle1"].Value != middle.Toggles["toggle1"].Value {
		t.Errorf("%q did not inherit properly from %q: value of toggle1 is: %v", bottom.Name, middle.Name, bottom.Toggles["toggle1"].Value)
	}
}

func BenchmarkStringValue(b *testing.B) {
	chamber := &Chamber{
		Name: "BOTTOM",
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "string",
					Value: "toggle1",
				},
			},
			"toggle2": {
				Toggle: &Toggle{
					Type:  "string",
					Value: "toggle2",
				},
			},
			"toggle3": {
				Toggle: &Toggle{
					Type:  "string",
					Value: "toggle3",
				},
			},
		},
		lock: new(sync.RWMutex),
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		chamber.StringValue("toggle1", "", "")
	}
}

func BenchmarkBoolValue(b *testing.B) {
	chamber := &Chamber{
		Name: "BOTTOM",
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: true,
				},
			},
			"toggle2": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: true,
				},
			},
			"toggle3": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: true,
				},
			},
		},
		lock: new(sync.RWMutex),
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		chamber.BoolValue("toggle1", false, "")
	}
}
