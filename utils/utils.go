package utils

import (
	"fmt"
	"os"

	rein "github.com/steviebps/rein/pkg"
)

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

// SaveAndExit Saves the chamber to the file specified
func SaveAndExit(file string, c rein.Chamber) {
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)

	if err != nil {
		fmt.Printf("Error opening file: %v\n", err)
		os.Exit(1)
	}
	c.Print(f, true)
	if err := f.Close(); err != nil {
		fmt.Printf("Error closing file: %v\n", err)
		os.Exit(1)
	}

	// Save complete
	os.Exit(0)
}
