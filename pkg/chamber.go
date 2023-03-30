package realm

import (
	"encoding/json"
	"errors"
	"sync"
)

// Chamber is a struct that holds metadata and toggles
type Chamber struct {
	Name        string                         `json:"name"`
	IsBuildable bool                           `json:"isBuildable"`
	IsApp       bool                           `json:"isApp"`
	Toggles     map[string]*OverrideableToggle `json:"toggles"`
	lock        *sync.RWMutex
}

type chamberAlias Chamber

// InheritWith will take a map of toggles to inherit from
// so that any toggles that do not exist in this chamber will be written to the map
func (c *Chamber) InheritWith(inherited map[string]*OverrideableToggle) {
	c.lock.Lock()
	defer c.lock.Unlock()
	for key := range inherited {
		if _, ok := c.Toggles[key]; !ok {
			c.Toggles[key] = inherited[key]
		}
	}
}

// TraverseAndBuild will traverse all Chambers while inheriting their parent Toggles and executes a callback on each Chamber node.
// Traversing will stop if callback returns true.
func (c *Chamber) TraverseAndBuild(callback func(Chamber) bool, children []*Chamber) {

	// if callback returns true, stop traversing
	// consumer was only looking to build up to this point
	if callback(*c) {
		return
	}

	for _, v := range children {
		v.InheritWith(c.Toggles)
	}
}

// GetToggleValue returns the toggle with the specified toggleName at the specified version.
// Will return nil if the toggle does not exist
func (c *Chamber) GetToggleValue(toggleName string, version string) interface{} {
	t := c.GetToggle(toggleName)
	if t == nil {
		return nil
	}
	return t.GetValueAt(version)
}

// GetToggle returns the toggle with the specified toggleName.
// Will return nil if the toggle does not exist
func (c *Chamber) GetToggle(toggleName string) *OverrideableToggle {
	c.lock.RLock()
	defer c.lock.RUnlock()
	t, ok := c.Toggles[toggleName]
	if !ok {
		return nil
	}
	return t
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *Chamber) StringValue(toggleKey string, defaultValue string, version string) (string, bool) {
	cStr, ok := c.GetToggleValue(toggleKey, version).(string)
	if !ok {
		return defaultValue, ok
	}
	return cStr, ok
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *Chamber) BoolValue(toggleKey string, defaultValue bool, version string) (bool, bool) {
	cBool, ok := c.GetToggleValue(toggleKey, version).(bool)
	if !ok {
		return defaultValue, ok
	}
	return cBool, ok
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *Chamber) Float64Value(toggleKey string, defaultValue float64, version string) (float64, bool) {
	cFloat64, ok := c.GetToggleValue(toggleKey, version).(float64)
	if !ok {
		return defaultValue, ok
	}
	return cFloat64, ok
}

// CustomValue retrieves a json.RawMessage by the key of the toggle
// and returns a bool on whether or not the toggle exists and is the proper type
func (c *Chamber) CustomValue(toggleKey string, version string) (*json.RawMessage, bool) {
	t, ok := c.GetToggleValue(toggleKey, version).(*json.RawMessage)
	if !ok {
		return nil, ok
	}
	return t, ok
}

// UnmarshalJSON Custom UnmarshalJSON method for validating Chamber
func (c *Chamber) UnmarshalJSON(b []byte) error {
	var alias chamberAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}

	*c = Chamber(alias)
	c.lock = &sync.RWMutex{}
	if c.Name == "" {
		return errors.New("all chambers must have a name")
	}

	return nil
}
