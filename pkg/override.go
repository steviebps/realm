package realm

import (
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/mod/semver"
)

// Override is a Toggle value to be consumed by and restricted to a semantic version range
type Override struct {
	*Toggle
	MinimumVersion string `json:"minimumVersion"`
	MaximumVersion string `json:"maximumVersion"`
}

// type overrideAlias Override

// UnmarshalJSON Custom UnmarshalJSON method for validating Override
func (o *Override) UnmarshalJSON(b []byte) error {
	var toggle Toggle
	err := json.Unmarshal(b, &toggle)
	if err != nil {
		return err
	}
	o.Toggle = &toggle

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	for k, v := range m {
		switch k {
		case "minimumVersion":
			var min string
			if err := json.Unmarshal(v, &min); err != nil {
				return err
			}
			o.MinimumVersion = min
		case "maximumVersion":
			var max string
			if err := json.Unmarshal(v, &max); err != nil {
				return err
			}
			o.MaximumVersion = max
		}
	}

	if o.Value == nil {
		return errors.New("Override value cannot be empty/nil")
	}

	if isValidMin := semver.IsValid(o.MinimumVersion); !isValidMin {
		return fmt.Errorf("%q is not a valid semantic version", o.MinimumVersion)
	}

	if isValidMax := semver.IsValid(o.MaximumVersion); !isValidMax {
		return fmt.Errorf("%q is not a valid semantic version", o.MaximumVersion)
	}

	// if minimum version is greater than maximum version
	if semver.Compare(o.MinimumVersion, o.MaximumVersion) == 1 {
		return fmt.Errorf("an override with the minimum version of %v is greater than its maximum version (%v)", o.MinimumVersion, o.MaximumVersion)
	}

	return nil
}
