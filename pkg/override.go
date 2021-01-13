package rein

import (
	"encoding/json"
	"errors"
)

// Override is a Toggle value to be consumed by and restricted to a semantic version range
type Override struct {
	MinimumVersion string      `json:"minimumVersion"`
	MaximumVersion string      `json:"maximumVersion"`
	Value          interface{} `json:"value"`
}

// UnmarshalJSON Custom UnmarshalJSON method for validating Override
func (o *Override) UnmarshalJSON(b []byte) error {

	var alias overrideAlias

	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}

	*o = alias.toOverride()

	minEmpty := o.MinimumVersion == ""
	maxEmpty := o.MaximumVersion == ""

	if minEmpty && maxEmpty {
		return errors.New("Override ranges cannot both be empty")
	}

	return nil
}

type overrideAlias Override

func (o overrideAlias) toOverride() Override {
	return Override{
		o.MinimumVersion,
		o.MaximumVersion,
		o.Value,
	}
}
