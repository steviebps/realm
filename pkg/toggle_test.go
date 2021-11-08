package realm

import (
	"testing"
)

func TestIsValidValue(t *testing.T) {

	tests := []struct {
		assertedType string
		input        interface{}
		output       bool
	}{
		{"boolean", false, true},
		{"boolean", "string", false},
		{"string", "string", true},
		{"string", 0, false},
		{"number", 1000.00, true},
		{"number", false, false},
	}
	toggle := Toggle{}

	for _, test := range tests {
		toggle.Type = test.assertedType
		got := toggle.IsValidValue(test.input)
		if got != test.output {
			t.Errorf("input: %v with asserted type: %v\nreturned %v expected: %v", test.input, test.assertedType, got, test.output)
		}
	}
}
