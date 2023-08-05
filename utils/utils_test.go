package utils

import (
	"testing"

	"golang.org/x/exp/slices"
)

func TestPathSplit(t *testing.T) {
	tests := []struct {
		path   string
		output []string
	}{
		{"", []string{}},
		{"/", []string{""}},
		{"/test/path", []string{"test", "path"}},
	}

	for _, test := range tests {
		output := pathSplit(test.path)
		if !slices.Equal(output, test.output) {
			t.Errorf("path: %q should return %q but returned %q", test.path, test.output, output)
		}
	}
}
