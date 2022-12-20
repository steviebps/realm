package realm

import (
	"encoding/json"
	"testing"
)

func TestAssertType(t *testing.T) {

	tests := []struct {
		assertedType  string
		input         json.RawMessage
		errorExpected bool
	}{
		{"boolean", []byte("false"), false},
		{"boolean", []byte("\"string\""), true},
		{"string", []byte("\"string\""), false},
		{"string", []byte("0"), true},
		{"number", []byte("1000.00"), false},
		{"number", []byte("false"), true},
	}

	for _, test := range tests {
		toggle := &Toggle{Type: test.assertedType}
		err := toggle.assertType(test.input)
		if err != nil && !test.errorExpected {
			t.Errorf("input: %v with asserted type: %v\nreturned %v", string(test.input), test.assertedType, err)
		}
	}
}
