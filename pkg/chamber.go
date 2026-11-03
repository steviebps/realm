package realm

import (
	"encoding/json"
	"maps"
)

// Chamber is a struct that holds metadata and rules
type Chamber struct {
	Rules map[string]*Flag `json:"rules"`
}

type chamberAlias Chamber

func (c *Chamber) UnmarshalJSON(b []byte) error {
	var alias chamberAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}

	*c = Chamber(alias)
	if c.Rules == nil {
		c.Rules = make(map[string]*Flag)
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

// EvaluatedFlag is the result of evaluating one flag for a context: its type
// and the value the context receives, without the rules that produced it.
type EvaluatedFlag struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// Evaluate resolves every flag in the chamber for ec.
func (c *Chamber) Evaluate(ec EvaluationContext) map[string]EvaluatedFlag {
	flags := make(map[string]EvaluatedFlag, len(c.Rules))
	for key, f := range c.Rules {
		if f == nil || f.Rule == nil {
			continue
		}
		flags[key] = EvaluatedFlag{Type: f.Type, Value: f.ValueFor(key, ec)}
	}
	return flags
}

// ChamberEntry is a read-only version of Chamber
// it is specifically used for realm clients
type ChamberEntry struct {
	rules      map[string]*Flag
	attributes map[string]string
}

// NewChamberEntry creates a new ChamberEntry. The provided attributes are
// process-wide defaults (for example "environment" or "version") consulted
// when an evaluation context does not set them.
func NewChamberEntry(c *Chamber, attributes map[string]string) *ChamberEntry {
	return &ChamberEntry{
		rules:      maps.Clone(c.Rules),
		attributes: maps.Clone(attributes),
	}
}

// Get returns the flag with the specified ruleKey.
// Will return nil if the flag does not exist
func (c *ChamberEntry) Get(ruleKey string) *Flag {
	t, ok := c.rules[ruleKey]
	if !ok {
		return nil
	}

	return t
}

// entryValue resolves ruleKey for ec (merged over the entry's process-wide
// attributes) using the typed accessor get, wrapping lookup and conversion
// failures in the chamber's error types.
func entryValue[T any](c *ChamberEntry, ruleKey string, ec EvaluationContext, defaultValue T, get func(*Flag, string, EvaluationContext, T) (T, bool)) (T, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	v, ok := get(t, ruleKey, ec.withDefaults(c.attributes), defaultValue)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	return v, nil
}

// StringValueFor retrieves a string by the key of the rule, resolved for the
// given evaluation context (applying any targeting rules). It returns the
// default value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) StringValueFor(ruleKey string, ec EvaluationContext, defaultValue string) (string, error) {
	return entryValue(c, ruleKey, ec, defaultValue, (*Flag).StringValueFor)
}

// StringValue retrieves a string by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) StringValue(ruleKey string, defaultValue string) (string, error) {
	return c.StringValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// BoolValueFor retrieves a bool by the key of the rule, resolved for the given
// evaluation context (applying any targeting rules). It returns the default
// value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) BoolValueFor(ruleKey string, ec EvaluationContext, defaultValue bool) (bool, error) {
	return entryValue(c, ruleKey, ec, defaultValue, (*Flag).BoolValueFor)
}

// BoolValue retrieves a bool by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) BoolValue(ruleKey string, defaultValue bool) (bool, error) {
	return c.BoolValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// Float64ValueFor retrieves a float64 by the key of the rule, resolved for the
// given evaluation context (applying any targeting rules). It returns the
// default value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) Float64ValueFor(ruleKey string, ec EvaluationContext, defaultValue float64) (float64, error) {
	return entryValue(c, ruleKey, ec, defaultValue, (*Flag).Float64ValueFor)
}

// Float64Value retrieves a float64 by the key of the rule
// and returns the default value if it does not exist and an error if it is not found or could not be converted
func (c *ChamberEntry) Float64Value(ruleKey string, defaultValue float64) (float64, error) {
	return c.Float64ValueFor(ruleKey, EvaluationContext{}, defaultValue)
}

// CustomValueFor retrieves a json.RawMessage by the key of the rule, resolved
// for the given evaluation context (applying any targeting rules), and
// unmarshals it into v. It returns an error if the rule is not found or could
// not be converted.
func (c *ChamberEntry) CustomValueFor(ruleKey string, ec EvaluationContext, v any) error {
	t := c.Get(ruleKey)
	if t == nil {
		return &ErrRuleNotFound{Key: ruleKey}
	}
	err := t.CustomValueFor(ruleKey, ec.withDefaults(c.attributes), v)
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
