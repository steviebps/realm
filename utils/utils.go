package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
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

func ReadInterfaceWith(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil && err != io.EOF {
		return err
	}

	return nil
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

func EnsureTrailingSlash(s string) string {
	s = strings.TrimSpace(s)

	if len(s) > 0 && s[len(s)-1] != '/' {
		s += "/"
	}
	return s
}
