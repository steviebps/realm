package realm

// Chamber is a struct that holds metadata and toggles
type Chamber struct {
	Toggles map[string]*OverrideableToggle `json:"toggles"`
}

// InheritWith will take a map of toggles to inherit from
// so that any toggles that do not exist in this chamber will be written to the map
func (c *Chamber) InheritWith(inherited map[string]*OverrideableToggle) {
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
// it is specifically used for realm clients
type ChamberEntry struct {
	toggles map[string]*OverrideableToggle
	version string
}

// NewChamberEntry creates a new ChamberEntry with the specified version
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
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) StringValue(toggleKey string, defaultValue string) (string, error) {
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.StringValue(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) BoolValue(toggleKey string, defaultValue bool) (bool, error) {
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.BoolValue(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) Float64Value(toggleKey string, defaultValue float64) (float64, error) {
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.Float64Value(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// CustomValue retrieves a json.RawMessage by the key of the toggle
// and returns an error if it is not found or could not be converted
func (c *ChamberEntry) CustomValue(toggleKey string, v any) error {
	t := c.Get(toggleKey)
	if t == nil {
		return &ErrToggleNotFound{toggleKey}
	}
	err := t.CustomValue(c.version, v)
	if err != nil {
		return &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return nil
}
