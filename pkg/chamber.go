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

// InheritFrom inherits rules from the provided chamber if they do not exist in the current chamber
func (c *Chamber) InheritFrom(from *Chamber) {
	for key := range from.Rules {
		if _, ok := c.Rules[key]; !ok {
			c.Rules[key] = from.Rules[key]
		}
	}
}

// OverwriteFrom overwrites rules from the provided chamber
func (c *Chamber) OverwriteFrom(overwrittenFrom *Chamber) {
	for key := range overwrittenFrom.Rules {
		c.Rules[key] = overwrittenFrom.Rules[key]
	}
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

// StringValueFor retrieves a string by the key of the rule, resolved for the
// given evaluation context (applying any percentage rollout). It returns the
// default value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) StringValueFor(ruleKey string, ec EvaluationContext, defaultValue string) (string, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.StringValueFor(ruleKey, ec, c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// StringValue retrieves a string by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) StringValue(ruleKey string, defaultValue string) (string, error) {
	return c.StringValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// BoolValueFor retrieves a bool by the key of the rule, resolved for the given
// evaluation context (applying any percentage rollout). It returns the default
// value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) BoolValueFor(ruleKey string, ec EvaluationContext, defaultValue bool) (bool, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.BoolValueFor(ruleKey, ec, c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// BoolValue retrieves a bool by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) BoolValue(ruleKey string, defaultValue bool) (bool, error) {
	return c.BoolValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// Float64ValueFor retrieves a float64 by the key of the rule, resolved for the
// given evaluation context (applying any percentage rollout). It returns the
// default value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) Float64ValueFor(ruleKey string, ec EvaluationContext, defaultValue float64) (float64, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := t.Float64ValueFor(ruleKey, ec, c.version, defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// Float64Value retrieves a float64 by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) Float64Value(ruleKey string, defaultValue float64) (float64, error) {
	return c.Float64ValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// CustomValueFor retrieves a json.RawMessage by the key of the rule, resolved
// for the given evaluation context (applying any percentage rollout), and
// unmarshals it into v. It returns an error if the rule is not found or could
// not be converted.
func (c *ChamberEntry) CustomValueFor(ruleKey string, ec EvaluationContext, v any) error {
	t := c.Get(ruleKey)
	if t == nil {
		return &ErrRuleNotFound{Key: ruleKey}
	}
	err := t.CustomValueFor(ruleKey, ec, c.version, v)
	if err != nil {
		return &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return nil
}

// CustomValue retrieves a json.RawMessage by the key of the rule
// and returns an error if it is not found or could not be converted
func (c *ChamberEntry) CustomValue(ruleKey string, v any) error {
	return c.CustomValueFor(ruleKey, EvaluationContext{}, v)
}
