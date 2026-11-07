package storage

import "testing"

func TestIsReservedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/_realm/", true},
		{"/_realm", true},
		{"_realm/keys/", true},
		{"/_realm/history/x/", true},
		// empty and "." segments are ignored
		{"//_realm/", true},
		{"/./_realm/", true},
		// a case-insensitive filesystem (the default on macOS and Windows)
		// maps these onto the same directory as "/_realm/"
		{"/_REALM/", true},
		{"/_Realm/keys/", true},
		{"//_rEaLm/history/x/", true},

		{"/", false},
		{"", false},
		{"/a/", false},
		// only the root-level prefix is reserved
		{"/a/_realm/", false},
		{"/a/_REALM/", false},
		// longer or shorter names are ordinary chambers
		{"/_realms/", false},
		{"/_real/", false},
		{"/x_realm/", false},
		{"/_realm_/", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsReservedPath(tt.path); got != tt.want {
				t.Errorf("IsReservedPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
