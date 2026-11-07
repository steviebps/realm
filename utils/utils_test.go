package utils

import (
	"slices"
	"testing"
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

func TestExpandEnvRef(t *testing.T) {
	t.Setenv("REALM_TEST_SECRET", "s3cret")
	for in, want := range map[string]string{
		"${REALM_TEST_SECRET}":       "s3cret",
		"$REALM_TEST_SECRET":         "s3cret",
		"${REALM_TEST_UNSET_SECRET}": "",
		"literal":                    "literal",
		"pa$$word":                   "pa$$word",
		"abc$REALM_TEST_SECRET":      "abc$REALM_TEST_SECRET", // only whole-value references expand
		"${REALM_TEST_SECRET}x":      "${REALM_TEST_SECRET}x",
		"${REALM_TEST_SECRET":        "${REALM_TEST_SECRET",
		"${1BAD}":                    "${1BAD}",
		"$":                          "$",
		"":                           "",
	} {
		if got := ExpandEnvRef(in); got != want {
			t.Errorf("ExpandEnvRef(%q) = %q, want %q", in, got, want)
		}
	}
}
