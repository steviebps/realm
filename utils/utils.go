package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

// ParseURL returns a url.URL when the passed string is a valid url with a host and scheme
func ParseURL(str string) (*url.URL, error) {
	u, err := url.Parse(str)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("URL string must contain a protocol scheme and host")
	}
	return u, nil
}

func WriteInterfaceWith(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}

	return enc.Encode(v)
}

// ReadInterfaceWith decodes a single JSON value from r into v, rejecting
// unknown fields. An empty input is not an error and leaves v untouched; use
// ReadRequiredInterfaceWith when a value must be present.
func ReadInterfaceWith(r io.Reader, v any) error {
	if err := ReadRequiredInterfaceWith(r, v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

// ReadRequiredInterfaceWith is like ReadInterfaceWith but returns io.EOF when r
// holds no JSON value, so callers can tell an empty body from an empty object.
func ReadRequiredInterfaceWith(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	return dec.Decode(v)
}

func OpenFile(fileName string) (io.ReadCloser, error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0755)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return file, fmt.Errorf("could not open file %q because it does not exist: %w", fileName, err)
		}
		return file, fmt.Errorf("could not open file %q: %w", fileName, err)
	}

	return file, nil
}

// ExpandEnvRef returns the value of the environment variable that s names as
// ${NAME} (or $NAME), or s itself when it is not such a reference. Only a
// reference that is the whole of s expands, so a literal secret containing
// "$" is used as written. An unset variable expands to "".
func ExpandEnvRef(s string) string {
	name, ok := strings.CutPrefix(s, "$")
	if !ok {
		return s
	}
	if inner, braced := strings.CutPrefix(name, "{"); braced {
		if name, ok = strings.CutSuffix(inner, "}"); !ok {
			return s
		}
	}
	if !isEnvName(name) {
		return s
	}
	return os.Getenv(name)
}

// isEnvName reports whether s is a portable environment variable name.
func isEnvName(s string) bool {
	if s == "" || ('0' <= s[0] && s[0] <= '9') {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c == '_', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		default:
			return false
		}
	}
	return true
}

func EnsureTrailingSlash(s string) string {
	s = strings.TrimSpace(s)

	if len(s) > 0 && s[len(s)-1] != '/' {
		s += "/"
	}
	return s
}

func pathSplit(p string) []string {
	dir, last := path.Split(p)
	if dir == "/" {
		return []string{last}
	}
	if dir == "" {
		return []string{}
	}
	return append(pathSplit(path.Clean(dir)), last)
}
