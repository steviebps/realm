package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteModule(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    []string // in the output, in order
		without []string // not in the output
		wantErr bool
	}{
		{
			name:  "license and notice",
			files: map[string]string{"LICENSE": "the license\n", "NOTICE.txt": "the notice", "README.md": "the readme"},
			want:  []string{"## example.com/m v1.0.0", "the license", "the notice"},
			// only the files a license asks redistributors to pass on
			without: []string{"the readme"},
		},
		{
			name:  "any spelling and case",
			files: map[string]string{"licence.md": "british", "COPYING": "gnu style"},
			want:  []string{"gnu style", "british"},
		},
		{
			name:  "license with a suffix",
			files: map[string]string{"LICENSE-APACHE": "apache", "LICENSE-MIT": "mit"},
			want:  []string{"apache", "mit"},
		},
		{
			name:    "a notice alone is not a license",
			files:   map[string]string{"NOTICE": "the notice"},
			wantErr: true,
		},
		{
			name:    "no license",
			files:   map[string]string{"README.md": "the readme"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// a directory named like a license file is skipped
			if err := os.Mkdir(filepath.Join(dir, "licenses"), 0o755); err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			err := writeModule(&b, module{Path: "example.com/m", Version: "v1.0.0", Dir: dir})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("writeModule() = nil, want an error; wrote:\n%s", b.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("writeModule() = %v", err)
			}
			out := b.String()
			at := 0
			for _, s := range tt.want {
				i := strings.Index(out[at:], s)
				if i < 0 {
					t.Fatalf("output lacks %q after offset %d:\n%s", s, at, out)
				}
				at += i + len(s)
			}
			for _, s := range tt.without {
				if strings.Contains(out, s) {
					t.Errorf("output has %q:\n%s", s, out)
				}
			}
		})
	}
}
