package realm

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Flag is a single feature flag: a typed default value plus an ordered list of
// targeting rules. The first targeting rule that applies to an evaluation
// context supplies the value; when none applies, the default is served.
//
// A flag of a type this version of realm does not know (one a newer realm
// added) decodes without error but cannot be served: typed getters return the
// caller's default with an error, and Chamber.Evaluate omits it. It is kept
// exactly as it was read, so that marshaling it again (as a server does when
// it patches a chamber) neither drops nor alters it.
//
// A sealed flag cannot be overridden by the chambers below the one that
// defines it: with inheritance, their definitions of its key are ignored, and
// the server refuses writes that add or change one.
type Flag struct {
	*Rule
	// Sealed keeps chambers below the one that defines the flag from
	// overriding it. It has no effect without inheritance.
	Sealed    bool             `json:"sealed,omitempty"`
	Targeting []*TargetingRule `json:"targeting,omitempty"`

	// raw holds a flag of an unknown type as it was read.
	raw json.RawMessage
}

// flagAlias is a Flag without its methods, for marshaling.
type flagAlias Flag

// removedFlagFields maps fields from earlier releases to migration guidance.
var removedFlagFields = map[string]string{
	"overrides": "version overrides were replaced by targeting rules with semverGte/semverLte clauses on the \"version\" attribute",
	"rollout":   "flag-level rollouts were replaced by a rollout on a targeting rule",
}

// UnmarshalJSON validates the flag's default value against its type, and each
// targeting rule's clauses, rollout, and value. A flag of an unknown type is
// kept as it is, unvalidated, though its seal is still read, so that a newer
// realm's sealed flag still seals.
func (f *Flag) UnmarshalJSON(b []byte) error {
	var rule Rule
	if err := json.Unmarshal(b, &rule); err != nil {
		return err
	}
	// decoded as encoding/json matches fields (case-insensitively), as
	// ParseChamber's field check does, so no spelling of it is ignored
	var seal struct {
		Sealed bool `json:"sealed"`
	}
	if err := json.Unmarshal(b, &seal); err != nil {
		return fmt.Errorf("sealed: %w", err)
	}
	f.Rule = &rule
	f.Sealed = seal.Sealed
	f.Targeting = nil
	f.raw = nil

	if !knownType(rule.Type) {
		f.raw = bytes.Clone(b)
		return nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	for field, guidance := range removedFlagFields {
		if _, ok := m[field]; ok {
			return fmt.Errorf("%q is no longer supported: %s", field, guidance)
		}
	}

	if v, ok := m["targeting"]; ok {
		targeting, err := parseTargeting(v, rule.Type)
		if err != nil {
			return err
		}
		f.Targeting = targeting
	}

	return nil
}

// MarshalJSON writes the flag. A flag of an unknown type is written exactly as
// it was read.
func (f Flag) MarshalJSON() ([]byte, error) {
	if f.raw != nil {
		return f.raw, nil
	}
	return json.Marshal(flagAlias(f))
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

// valueAs resolves the flag for ec and asserts the result to T, returning
// defaultValue and false when it is not a T.
func valueAs[T any](f *Flag, flagKey string, ec EvaluationContext, defaultValue T) (T, bool) {
	v, ok := f.ValueFor(flagKey, ec).(T)
	if !ok {
		return defaultValue, false
	}
	return v, true
}

// StringValueFor retrieves a string value of the flag for the given evaluation
// context and returns the default value if it could not be converted, along
// with whether the conversion succeeded.
func (f *Flag) StringValueFor(flagKey string, ec EvaluationContext, defaultValue string) (string, bool) {
	return valueAs(f, flagKey, ec, defaultValue)
}

// BoolValueFor retrieves a bool value of the flag for the given evaluation
// context and returns the default value if it could not be converted, along
// with whether the conversion succeeded.
func (f *Flag) BoolValueFor(flagKey string, ec EvaluationContext, defaultValue bool) (bool, bool) {
	return valueAs(f, flagKey, ec, defaultValue)
}

// Float64ValueFor retrieves a float64 value of the flag for the given
// evaluation context and returns the default value if it could not be
// converted, along with whether the conversion succeeded.
func (f *Flag) Float64ValueFor(flagKey string, ec EvaluationContext, defaultValue float64) (float64, bool) {
	return valueAs(f, flagKey, ec, defaultValue)
}

// CustomValueFor unmarshals the flag value for the given evaluation context
// into v. Only a flag of type "custom" has one.
func (f *Flag) CustomValueFor(flagKey string, ec EvaluationContext, v any) error {
	if f.Type != "custom" {
		return fmt.Errorf("flag with type %q could not be converted for unmarshalling", f.Type)
	}
	raw, ok := f.ValueFor(flagKey, ec).(*json.RawMessage)
	if !ok || raw == nil {
		return fmt.Errorf("flag with type %q could not be converted for unmarshalling", f.Type)
	}
	return json.Unmarshal(*raw, v)
}
