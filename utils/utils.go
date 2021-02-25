package utils

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"

	rein "github.com/steviebps/rein/pkg"
)

// IsURL returns whether the string is a valid url with a host and scheme
func IsURL(str string) (bool, *url.URL) {
	u, err := url.Parse(str)
	return err == nil && u.Scheme != "" && u.Host != "", u
}

// FindExists returns the first file that exists
func FindExists(names []string) string {
	for _, file := range names {
		if Exists(file) {
			return file
		}
	}
	return ""
}

// Exists reports whether the named file or directory exists.
func Exists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// WriteChamberToFile Saves the chamber to the file specified
func WriteChamberToFile(fileName string, c rein.Chamber, pretty bool) error {
	bw, file := OpenFileWriter(fileName)
	c.EncodeWith(bw, pretty)
	bw.Flush()

	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func OpenFileWriter(fileName string) (*bufio.Writer, *os.File) {
	file, err := os.OpenFile(fileName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("Error opening file: %v\n", err)
		os.Exit(1)
	}
	return bufio.NewWriter(file), file
}

func WriteInterfaceToFile(fileName string, i interface{}, pretty bool) {
	bw, file := OpenFileWriter(fileName)
	enc := json.NewEncoder(bw)

	if pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(i); err != nil {
		fmt.Printf("Encoding error: %v\n", err)
	}

	bw.Flush()

	if err := file.Close(); err != nil {
		fmt.Printf("Error closing file: %v\n", err)
		os.Exit(1)
	}
}

func WriteInterfaceWith(w io.Writer, i interface{}, pretty bool) {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)

	if pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(i); err != nil {
		fmt.Printf("Encoding error: %v\n", err)
	}

	bw.Flush()
}
