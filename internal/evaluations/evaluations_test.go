package evaluations

import (
	"strings"
	"testing"
)

// TestOfFindsASeriesWithoutAllocating checks that finding a series that
// exists, as the server does for every flag it serves, allocates nothing.
func TestOfFindsASeriesWithoutAllocating(t *testing.T) {
	// built at run time, as the server's request paths and flag keys are
	path, flag := strings.Repeat("/p", 2)+"/", strings.Repeat("f", 3)
	s := Of(path, flag, "default")
	if again := Of(strings.Clone(path), flag, "default"); again != s {
		t.Fatal("Of returned another series for the same path, flag, and rule")
	}
	if Of(path, flag, "#1") == s {
		t.Fatal("Of returned the same series for another rule")
	}
	if n := testing.AllocsPerRun(100, func() { Of(path, flag, "default").Add() }); n != 0 {
		t.Errorf("Of allocates %v times per call, want 0", n)
	}
}
