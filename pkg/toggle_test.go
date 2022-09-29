package realm

import (
	"testing"
)

func TestAssertType(t *testing.T) {

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

	for _, test := range tests {
		ok := assertType(test.assertedType, test.input)
		if ok != test.output {
			t.Errorf("input: %v with asserted type: %v\nreturned %v expected: %v", test.input, test.assertedType, ok, test.output)
		}
	}
}
