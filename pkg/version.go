package rein

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version a semantic version
type Version struct {
	Major, Minor, Patch uint64
	Pre                 string
	Metadata            string
	literal             string
}

// NewVersion retuns a Version struct with a given string
func NewVersion(v string) (*Version, error) {

	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		errMsg := fmt.Sprintf("Invalid semantic version. %q must contain at least 3 parts (X.X.X)", v)
		return nil, errors.New(errMsg)
	}

	version := &Version{
		literal: v,
	}

	var err error
	version.Major, err = strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, err
	}

	version.Minor, err = strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil, err
	}

	version.Patch, err = strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return nil, err
	}

	return version, nil
}
