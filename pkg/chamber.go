package realm

import (
	"encoding/json"
	"sync"
)

// Chamber is a struct that holds metadata and toggles
type Chamber struct {
	Toggles map[string]*OverrideableToggle `json:"toggles"`
	lock    *sync.RWMutex
}

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
// func (c *Chamber) TraverseAndBuild(callback func(Chamber) bool, children []*Chamber) {

// 	// if callback returns true, stop traversing
// 	// consumer was only looking to build up to this point
// 	if callback(*c) {
// 		return
// 	}

// 	for _, v := range children {
// 		v.InheritWith(c.Toggles)
// 	}
// }

// ChamberEntry is a read-only version of Chamber
type ChamberEntry struct {
	toggles map[string]*OverrideableToggle
	version string
}

func NewChamberEntry(c *Chamber, version string) *ChamberEntry {
	m := make(map[string]*OverrideableToggle)
	for k, v := range c.Toggles {
		m[k] = v
	}

	return &ChamberEntry{
		toggles: m,
		version: version,
	}
}

// GetToggleValue returns the toggle with the specified toggleName at the specified version.
// Will return nil if the toggle does not exist
func (c *ChamberEntry) GetToggleValue(toggleName string) interface{} {
	t := c.Get(toggleName)
	if t == nil {
		return nil
	}
	return t.GetValueAt(c.version)
}

// Get returns the toggle with the specified toggleName.
// Will return nil if the toggle does not exist
func (c *ChamberEntry) Get(toggleName string) *OverrideableToggle {
	t, ok := c.toggles[toggleName]
	if !ok {
		return nil
	}

	return t
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *ChamberEntry) StringValue(toggleKey string, defaultValue string) (string, bool) {
	cStr, ok := c.GetToggleValue(toggleKey).(string)
	if !ok {
		return defaultValue, ok
	}
	return cStr, ok
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *ChamberEntry) BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	cBool, ok := c.GetToggleValue(toggleKey).(bool)
	if !ok {
		return defaultValue, ok
	}
	return cBool, ok
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (c *ChamberEntry) Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	cFloat64, ok := c.GetToggleValue(toggleKey).(float64)
	if !ok {
		return defaultValue, ok
	}
	return cFloat64, ok
}

// CustomValue retrieves a json.RawMessage by the key of the toggle
// and returns a bool on whether or not the toggle exists and is the proper type
func (c *ChamberEntry) CustomValue(toggleKey string, version string) (*json.RawMessage, bool) {
	t, ok := c.GetToggleValue(toggleKey).(*json.RawMessage)
	if !ok {
		return nil, ok
	}
	return t, ok
}
