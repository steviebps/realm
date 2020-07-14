package utils

import (
	"bufio"
	"fmt"
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
func WriteChamberToFile(file string, c rein.Chamber, pretty bool) {
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("Error opening file: %v\n", err)
		os.Exit(1)
	}
	bw := bufio.NewWriter(f)
	c.WriteWith(bw, pretty)
	bw.Flush()
	if err := f.Close(); err != nil {
		fmt.Printf("Error closing file: %v\n", err)
		os.Exit(1)
	}
}
