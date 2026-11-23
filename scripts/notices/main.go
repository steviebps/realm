// Command notices writes the third-party notices that ship with realm's
// release archives and container image: the license and notice files of
// every Go module compiled into the binary, then the licenses of the
// packages bundled into the web UI, which the UI build writes to
// http/realm-ui/dist/licenses.md.
//
// Run it from the repository root after building the UI:
//
//	go run ./scripts/notices -o THIRD_PARTY_NOTICES.md
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// platforms are the operating systems realm is released for. A module can be
// compiled in on only some of them, as cobra's Windows-only dependency is.
var platforms = []string{"linux", "darwin", "windows"}

// module is the part of a module go list reports that the notices use.
type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

func main() {
	out := flag.String("o", "", "write the notices to this file instead of standard output")
	ui := flag.String("ui", "http/realm-ui/dist/licenses.md", "the web UI's license file, written by its build; empty to leave the UI out")
	flag.Parse()
	if err := run(*out, *ui); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(out, ui string) error {
	mods, err := modules()
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString("# Third-party notices\n\n" +
		"realm is licensed under the Apache License 2.0 (LICENSE.txt). It includes\n" +
		"the third-party software below, each under its own license.\n\n" +
		"# Go modules\n")
	for _, m := range mods {
		if err := writeModule(&b, m); err != nil {
			return err
		}
	}
	if ui != "" {
		text, err := os.ReadFile(ui)
		if err != nil {
			return fmt.Errorf("reading the web UI's licenses (build the UI first): %w", err)
		}
		// Vite titles the file "# Licenses"
		text, _ = bytes.CutPrefix(text, []byte("# Licenses\n"))
		b.WriteString("\n# Web UI packages\n")
		b.Write(text)
	}
	if out == "" {
		_, err = os.Stdout.Write(b.Bytes())
		return err
	}
	return os.WriteFile(out, b.Bytes(), 0o644)
}

// modules returns the modules compiled into realm's release binary (built
// with the ui tag) on any of platforms, sorted by path.
func modules() ([]module, error) {
	byPath := map[string]module{}
	for _, goos := range platforms {
		cmd := exec.Command("go", "list", "-deps", "-tags=ui", "-json=Module", ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("listing the packages compiled in on %s: %w", goos, err)
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var pkg struct{ Module *module }
			if err := dec.Decode(&pkg); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("reading go list's output: %w", err)
			}
			// standard library packages have no module
			if pkg.Module != nil && !pkg.Module.Main {
				byPath[pkg.Module.Path] = *pkg.Module
			}
		}
	}
	mods := make([]module, 0, len(byPath))
	for _, m := range byPath {
		mods = append(mods, m)
	}
	slices.SortFunc(mods, func(a, b module) int { return strings.Compare(a.Path, b.Path) })
	return mods, nil
}

// writeModule writes m's heading and the contents of its notice files. It
// fails when m has no license file, so a release never ships notices that
// leave a module out.
func writeModule(b *bytes.Buffer, m module) error {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return fmt.Errorf("reading module %s: %w", m.Path, err)
	}
	fmt.Fprintf(b, "\n## %s %s\n", m.Path, m.Version)
	licensed := false
	for _, e := range entries {
		kind := noticeKind(e.Name())
		if e.IsDir() || kind == "" {
			continue
		}
		text, err := os.ReadFile(m.Dir + string(os.PathSeparator) + e.Name())
		if err != nil {
			return fmt.Errorf("reading module %s: %w", m.Path, err)
		}
		licensed = licensed || kind == "license"
		fmt.Fprintf(b, "\n%s\n", bytes.TrimSpace(text))
	}
	if !licensed {
		return fmt.Errorf("module %s has no license file in %s", m.Path, m.Dir)
	}
	return nil
}

// noticeKind reports whether the file name is a module's license ("license":
// LICENSE, LICENCE, COPYING, with any suffix) or its notice ("notice": the
// NOTICE file Apache-2.0 asks redistributors to pass on), or neither ("").
func noticeKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "license"), strings.HasPrefix(n, "licence"), strings.HasPrefix(n, "copying"):
		return "license"
	case strings.HasPrefix(n, "notice"):
		return "notice"
	}
	return ""
}
