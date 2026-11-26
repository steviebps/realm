package realm

import (
	"bytes"
	"encoding/json"
	"errors"
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
//
// Turning a flag's targeting off moves its rules from Targeting to
// PausedTargeting, which is never evaluated, so every context gets the
// default value; turning it on moves them back. (An SDK that predates
// PausedTargeting ignores the field, and so serves the default too.)
//
// Description, Owner, and Lifecycle say what the flag is for; nothing
// enforces them. Constraints restrict the values the flag may take: a write
// is refused when its default value or any targeting rule's value, paused or
// not, falls outside them, or outside the constraints of the flag in any
// chamber above (see ParseChamber, and the server). Reads never check them.
type Flag struct {
	*Rule
	// Description says what the flag is for.
	Description string `json:"description,omitempty"`
	// Owner names who is responsible for the flag: a team or a person.
	Owner string `json:"owner,omitempty"`
	// Lifecycle says whether the flag is meant to be removed once it has
	// served its purpose. Empty when unset.
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
	// Constraints is a schema (see Schema) the flag's default value and each
	// targeting rule's value must satisfy, kept as it was read so that a
	// newer realm's keywords survive an older one's rewrite. ParseChamber
	// checks it; ParseConstraints decodes it.
	Constraints json.RawMessage `json:"constraints,omitempty"`
	// Sealed keeps chambers below the one that defines the flag from
	// overriding it. It has no effect without inheritance.
	Sealed    bool             `json:"sealed,omitempty"`
	Targeting []*TargetingRule `json:"targeting,omitempty"`
	// PausedTargeting holds the flag's targeting rules while its targeting is
	// turned off. They are validated as Targeting is, but never evaluated.
	// ParseChamber refuses a flag with rules in both.
	PausedTargeting []*TargetingRule `json:"pausedTargeting,omitempty"`

	// raw holds a flag of an unknown type as it was read.
	raw json.RawMessage
}

// flagAlias is a Flag without its methods, for marshaling.
type flagAlias Flag

// Lifecycle says whether a flag is meant to be removed once it has served its
// purpose.
type Lifecycle string

const (
	// LifecycleTemporary marks a flag meant to be removed, such as a release
	// toggle or an experiment.
	LifecycleTemporary Lifecycle = "temporary"
	// LifecyclePermanent marks a flag meant to stay, such as a setting or a
	// kill switch.
	LifecyclePermanent Lifecycle = "permanent"
)

// known reports whether this version of realm knows l. Reads accept any
// lifecycle; ParseChamber refuses one it does not know.
func (l Lifecycle) known() bool {
	return l == "" || l == LifecycleTemporary || l == LifecyclePermanent
}

// flagFields holds a flag's fields beside its Rule, decoded from any flag,
// whatever its type, with its targeting rules kept raw so that they can be
// type-checked against the flag's type.
type flagFields struct {
	Description     string          `json:"description"`
	Owner           string          `json:"owner"`
	Lifecycle       Lifecycle       `json:"lifecycle"`
	Constraints     json.RawMessage `json:"constraints"`
	Sealed          bool            `json:"sealed"`
	Targeting       json.RawMessage `json:"targeting"`
	PausedTargeting json.RawMessage `json:"pausedTargeting"`
}

// removedFlagFields maps fields from earlier releases to migration guidance.
var removedFlagFields = map[string]string{
	"overrides": "version overrides were replaced by targeting rules with semverGte/semverLte clauses on the \"version\" attribute",
	"rollout":   "flag-level rollouts were replaced by a rollout on a targeting rule",
}

// UnmarshalJSON validates the flag's default value against its type, and each
// targeting rule's clauses, rollout, and value, paused or not. It does not
// check the flag's constraints or lifecycle (ParseChamber does), so that a
// stored value never stops a chamber from being read. A flag of an unknown
// type is kept as it is, unvalidated, though its seal and description are
// still read, so that a newer realm's sealed flag still seals.
func (f *Flag) UnmarshalJSON(b []byte) error {
	var rule Rule
	if err := json.Unmarshal(b, &rule); err != nil {
		return err
	}
	// decoded as encoding/json matches fields (case-insensitively), as
	// ParseChamber's field check does, so no spelling of one is ignored
	var meta flagFields
	if err := json.Unmarshal(b, &meta); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return fmt.Errorf("%s: %w", te.Field, err)
		}
		return err
	}
	f.Rule = &rule
	f.Description = meta.Description
	f.Owner = meta.Owner
	f.Lifecycle = meta.Lifecycle
	f.Constraints = nil
	if len(meta.Constraints) > 0 && !isNull(meta.Constraints) {
		f.Constraints = meta.Constraints
	}
	f.Sealed = meta.Sealed
	f.Targeting = nil
	f.PausedTargeting = nil
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

	if meta.Targeting != nil {
		targeting, err := parseTargeting(meta.Targeting, rule.Type)
		if err != nil {
			return err
		}
		f.Targeting = targeting
	}
	if meta.PausedTargeting != nil {
		paused, err := parseTargeting(meta.PausedTargeting, rule.Type)
		if err != nil {
			return fmt.Errorf("pausedTargeting: %w", err)
		}
		f.PausedTargeting = paused
	}

	return nil
}

// ParseConstraints decodes the flag's Constraints strictly, as ParseSchema
// does, for a flag of its type: the schema of a "number", "string", or
// "boolean" flag must be about values of that type. It returns nil when the
// flag has no constraints.
func (f *Flag) ParseConstraints() (*Schema, error) {
	if len(f.Constraints) == 0 {
		return nil, nil
	}
	if f.Rule == nil {
		return nil, errors.New("a flag with constraints must have a type")
	}
	return parseFlagSchema(f.Constraints, f.Type)
}

// CheckValues reports the first of the flag's values that s refuses: its
// default value, then each targeting rule's value, then each paused targeting
// rule's. A nil s allows every value.
func (f *Flag) CheckValues(s *Schema) error {
	if s == nil || f.Rule == nil {
		return nil
	}
	if err := checkValue(s, f.Value); err != nil {
		return fmt.Errorf("default value: %w", err)
	}
	for _, rules := range []struct {
		name  string
		rules []*TargetingRule
	}{{"targeting rule", f.Targeting}, {"paused targeting rule", f.PausedTargeting}} {
		for i, tr := range rules.rules {
			if err := checkValue(s, tr.Value); err != nil {
				return fmt.Errorf("%s %s: %w", rules.name, ruleName(tr.ID, i), err)
			}
		}
	}
	return nil
}

// checkValue checks v, a flag or targeting rule value as Flag.UnmarshalJSON
// holds it, against s.
func checkValue(s *Schema, v any) error {
	if raw, ok := v.(*json.RawMessage); ok {
		// a custom value, kept raw
		if raw == nil {
			return s.Validate(nil)
		}
		var decoded any
		if err := json.Unmarshal(*raw, &decoded); err != nil {
			return err
		}
		v = decoded
	}
	return s.Validate(v)
}

// TargetingPaused reports whether the flag's targeting is turned off: it has
// rules in PausedTargeting and none in Targeting, so every context gets its
// default value.
func (f *Flag) TargetingPaused() bool {
	return len(f.Targeting) == 0 && len(f.PausedTargeting) > 0
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
// of the first targeting rule that applies, or the default value. Paused
// targeting rules never apply.
func (f *Flag) ValueFor(flagKey string, ec EvaluationContext) interface{} {
	v, _ := f.resolve(flagKey, ec)
	return v
}

// resolve returns the value of the first targeting rule that applies to ec
// and the rule's index in Targeting, or the default value and -1.
func (f *Flag) resolve(flagKey string, ec EvaluationContext) (interface{}, int) {
	for i, tr := range f.Targeting {
		if tr.Applies(flagKey, ec) {
			return tr.Value, i
		}
	}
	return f.Value, -1
}

// valueAs resolves the flag for ec and asserts the result to T, returning
// defaultValue and false when it is not a T.
func valueAs[T any](f *Flag, flagKey string, ec EvaluationContext, defaultValue T) (T, bool) {
	v, _, ok := resolveAs(f, flagKey, ec, defaultValue)
	return v, ok
}

// resolveAs resolves the flag for ec as resolve does, and asserts the value
// to T, returning defaultValue and false when it is not a T.
func resolveAs[T any](f *Flag, flagKey string, ec EvaluationContext, defaultValue T) (T, int, bool) {
	v, rule := f.resolve(flagKey, ec)
	t, ok := v.(T)
	if !ok {
		return defaultValue, rule, false
	}
	return t, rule, true
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
	_, err := f.customValue(flagKey, ec, v)
	return err
}

// customValue unmarshals the flag value for ec into v, as CustomValueFor
// does, and returns the index of the targeting rule that served it, or -1
// for the default.
func (f *Flag) customValue(flagKey string, ec EvaluationContext, v any) (int, error) {
	if f.Type != "custom" {
		return -1, fmt.Errorf("flag with type %q could not be converted for unmarshalling", f.Type)
	}
	value, rule := f.resolve(flagKey, ec)
	raw, ok := value.(*json.RawMessage)
	if !ok || raw == nil {
		return rule, fmt.Errorf("flag with type %q could not be converted for unmarshalling", f.Type)
	}
	return rule, json.Unmarshal(*raw, v)
}
