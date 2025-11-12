package realm

import (
	"encoding/json"
)

// Chamber is a struct that holds metadata and rules
type Chamber struct {
	Rules map[string]*OverrideableRule `json:"rules"`
}

type chamberAlias Chamber

func (c *Chamber) UnmarshalJSON(b []byte) error {
	var alias chamberAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}

	*c = Chamber(alias)
	if c.Rules == nil {
		c.Rules = make(map[string]*OverrideableRule)
	}

	return nil
}

// ChamberEntry is a read-only version of Chamber
// it is specifically used for realm clients
type ChamberEntry struct {
	rules   map[string]*OverrideableRule
	version string
}

// NewChamberEntry creates a new ChamberEntry with the specified version
func NewChamberEntry(c *Chamber, version string) *ChamberEntry {
	m := make(map[string]*OverrideableRule)
	for k, v := range c.Rules {
		m[k] = v
	}

	return &ChamberEntry{
		rules:   m,
		version: version,
	}
}

// Get returns the rule with the specified ruleKey.
// Will return nil if the rule does not exist
func (c *ChamberEntry) Get(ruleKey string) *OverrideableRule {
	t, ok := c.rules[ruleKey]
	if !ok {
		return nil
	}

	return t
}

// StringValue retrieves a string by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) StringValue(ruleKey string, defaultValue string) (string, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.StringValue(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// BoolValue retrieves a bool by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) BoolValue(ruleKey string, defaultValue bool) (bool, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.BoolValue(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// Float64Value retrieves a float64 by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) Float64Value(ruleKey string, defaultValue float64) (float64, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.Float64Value(c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// CustomValue retrieves a json.RawMessage by the key of the rule
// and returns an error if it is not found or could not be converted
func (c *ChamberEntry) CustomValue(ruleKey string, v any) error {
	t := c.Get(ruleKey)
	if t == nil {
		return &ErrRuleNotFound{Key: ruleKey}
	}
	err := t.CustomValue(c.version, v)
	if err != nil {
		return &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return nil
}
