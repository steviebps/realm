package realm

import (
	"encoding/json"
	"fmt"
)

// Flag is a single feature flag: a typed default value plus an ordered list of
// targeting rules. The first targeting rule that applies to an evaluation
// context supplies the value; when none applies, the default is served.
type Flag struct {
	*Rule
	Targeting []*TargetingRule `json:"targeting,omitempty"`
}

// removedFlagFields maps fields from earlier releases to migration guidance.
var removedFlagFields = map[string]string{
	"overrides": "version overrides were replaced by targeting rules with semverGte/semverLte clauses on the \"version\" attribute",
	"rollout":   "flag-level rollouts were replaced by a rollout on a targeting rule",
}

// UnmarshalJSON validates the flag's default value against its type, and each
// targeting rule's clauses, rollout, and value.
func (f *Flag) UnmarshalJSON(b []byte) error {
	var rule Rule
	if err := json.Unmarshal(b, &rule); err != nil {
		return err
	}
	f.Rule = &rule

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	for field, guidance := range removedFlagFields {
		if _, ok := m[field]; ok {
			return fmt.Errorf("%q is no longer supported: %s", field, guidance)
		}
	}

	f.Targeting = nil
	if v, ok := m["targeting"]; ok {
		targeting, err := parseTargeting(v, rule.Type)
		if err != nil {
			return err
		}
		f.Targeting = targeting
	}

	return nil
}

// ValueFor resolves the flag value for the given evaluation context: the value
// of the first targeting rule that applies, or the default value.
func (f *Flag) ValueFor(flagKey string, ec EvaluationContext) interface{} {
	for _, tr := range f.Targeting {
		if tr.Applies(flagKey, ec) {
			return tr.Value
		}
	}
	return f.Value
}

// StringValueFor retrieves a string value of the flag for the given evaluation
// context and returns the default value if it could not be converted, along
// with whether the conversion succeeded.
func (f *Flag) StringValueFor(flagKey string, ec EvaluationContext, defaultValue string) (string, bool) {
	v, ok := f.ValueFor(flagKey, ec).(string)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// BoolValueFor retrieves a bool value of the flag for the given evaluation
// context and returns the default value if it could not be converted, along
// with whether the conversion succeeded.
func (f *Flag) BoolValueFor(flagKey string, ec EvaluationContext, defaultValue bool) (bool, bool) {
	v, ok := f.ValueFor(flagKey, ec).(bool)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// Float64ValueFor retrieves a float64 value of the flag for the given
// evaluation context and returns the default value if it could not be
// converted, along with whether the conversion succeeded.
func (f *Flag) Float64ValueFor(flagKey string, ec EvaluationContext, defaultValue float64) (float64, bool) {
	v, ok := f.ValueFor(flagKey, ec).(float64)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// CustomValueFor unmarshals the flag value for the given evaluation context
// into v.
func (f *Flag) CustomValueFor(flagKey string, ec EvaluationContext, v any) error {
	raw, ok := f.ValueFor(flagKey, ec).(*json.RawMessage)
	if !ok {
		return fmt.Errorf("flag with type %q could not be converted for unmarshalling", f.Type)
	}
	return json.Unmarshal(*raw, v)
}
