package utils

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
)

// IsURL returns whether the string is a valid url with a host and scheme
func IsURL(str string) (bool, *url.URL) {
	u, err := url.Parse(str)
	return err == nil && u.Scheme != "" && u.Host != "", u
}

func WriteInterfaceWith(w io.Writer, i interface{}, pretty bool) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)

	if pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(i); err != nil {
		return err
	}

	bw.Flush()
	return nil
}

func ReadInterfaceWith(r io.Reader, i interface{}) error {
	br := bufio.NewReader(r)
	dec := json.NewDecoder(br)

	if err := dec.Decode(i); err != nil && err != io.EOF {
		return err
	}

	return nil
}

func OpenLocalConfig(fileName string) (io.ReadCloser, error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0755)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not open file %q because it does not exist: %w", fileName, err)
		}
		return nil, fmt.Errorf("could not open file %q: %w", fileName, err)
	}

	return file, nil
}
