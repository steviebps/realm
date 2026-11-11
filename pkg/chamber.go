package realm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
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

// Evaluate resolves every flag in the chamber for ec, evaluating every
// schedule at the same instant. Flags of a type this version of realm does not
// know are omitted. It also returns the next time a schedule could change
// those values, or the zero time when none can, so a caller holding the values
// need only re-evaluate then.
func (c *Chamber) Evaluate(ec EvaluationContext) (map[string]EvaluatedFlag, time.Time) {
	if ec.at.IsZero() {
		ec.at = time.Now()
	}
	flags := make(map[string]EvaluatedFlag, len(c.Rules))
	for key, f := range c.Rules {
		if f == nil || f.Rule == nil || !knownType(f.Type) {
			continue
		}
		flags[key] = EvaluatedFlag{Type: f.Type, Value: f.ValueFor(key, ec)}
	}
	return flags, c.nextScheduleChange(ec.at)
}

// nextScheduleChange returns the earliest schedule bound of any targeting rule
// in the chamber that is after t, or the zero time when there is none.
func (c *Chamber) nextScheduleChange(t time.Time) time.Time {
	var next time.Time
	for _, f := range c.Rules {
		if f == nil {
			continue
		}
		for _, tr := range f.Targeting {
			if tr.Schedule == nil {
				continue
			}
			for _, bound := range []time.Time{tr.Schedule.Start, tr.Schedule.End} {
				if bound.After(t) && (next.IsZero() || bound.Before(next)) {
					next = bound
				}
			}
		}
	}
	return next
}

// ChamberEntry is a read-only version of Chamber
// it is specifically used for realm clients
type ChamberEntry struct {
	rules      map[string]*Flag
	attributes map[string]string
	// version caches the "version" attribute and its canonical semver form,
	// so semver clauses on the process-wide version do not allocate.
	version semverCache
	// revision is the server's storage revision of the chamber; 0 when
	// unknown.
	revision uint64
}

// Revision returns the server's storage revision of the chamber the entry
// was built from (see Realm.Revision), or 0 when the server did not report
// one.
func (c *ChamberEntry) Revision() uint64 {
	return c.revision
}

// NewChamberEntry creates a new ChamberEntry. The provided attributes are
// process-wide defaults (for example "environment" or "version") consulted
// when an evaluation context does not set them.
func NewChamberEntry(c *Chamber, attributes map[string]string) *ChamberEntry {
	v := attributes[VersionAttribute]
	return &ChamberEntry{
		rules:      maps.Clone(c.Rules),
		attributes: maps.Clone(attributes),
		version:    semverCache{raw: v, canonical: canonicalSemver(v)},
	}
}

// context returns ec falling back to the entry's process-wide attributes.
func (c *ChamberEntry) context(ec EvaluationContext) EvaluationContext {
	ec = ec.withDefaults(c.attributes)
	ec.version = c.version
	return ec
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
	v, ok := get(t, ruleKey, c.context(ec), defaultValue)
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
	err := t.CustomValueFor(ruleKey, c.context(ec), v)
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

// ParseChamber decodes and validates a chamber strictly, as the server does
// for writes. It is json.Unmarshal into a Chamber, except that a field,
// operator, or flag type realm does not know, at any level, is an error
// instead of being ignored: a misspelled "schedule" would otherwise make its
// rule apply immediately, a misspelled "rules" would replace the chamber with
// an empty one, and a misspelled operator would never match. (Reads decode
// leniently, so a newer server's additions do not break older SDKs.)
func ParseChamber(b []byte) (*Chamber, error) {
	var c Chamber
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if err := checkFields(b, chamberShape, ""); err != nil {
		return nil, err
	}
	if err := c.checkSupported(); err != nil {
		return nil, err
	}
	return &c, nil
}

// checkSupported reports the first flag, in key order, of a type or with an
// operator this version of realm does not know.
func (c *Chamber) checkSupported() error {
	for _, key := range slices.Sorted(maps.Keys(c.Rules)) {
		f := c.Rules[key]
		if f == nil || f.Rule == nil {
			continue
		}
		if !knownType(f.Type) {
			return fmt.Errorf("flag %q: %w", key, &UnsupportedTypeError{RuleType: f.Type})
		}
		for i, tr := range f.Targeting {
			for _, cl := range tr.Clauses {
				if !cl.Op.known() {
					return fmt.Errorf("flag %q: targeting rule %s: clause on %q: unsupported operator %q", key, ruleName(tr.ID, i), cl.Attribute, cl.Op)
				}
			}
		}
	}
	return nil
}

// unsupportedFlags returns, sorted, the keys of the flags this version of
// realm cannot fully evaluate: those of an unknown type, which cannot be
// served, and those with a clause of an unknown operator, which never matches.
func (c *Chamber) unsupportedFlags() []string {
	var keys []string
	for key, f := range c.Rules {
		if f == nil || f.Rule == nil {
			continue
		}
		if !knownType(f.Type) || slices.ContainsFunc(f.Targeting, (*TargetingRule).hasUnknownOperator) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// shape lists the fields a JSON object may hold, and the shapes of nested
// objects (or of the elements of nested arrays or maps) worth checking.
type shape struct {
	fields []string
	nested map[string]*shape
	// each applies to every value of a map-typed object (the chamber's rules).
	each *shape
}

var (
	clauseShape   = &shape{fields: []string{"attribute", "op", "values"}}
	rolloutShape  = &shape{fields: []string{"percentage", "bucketBy", "seed"}}
	scheduleShape = &shape{fields: []string{"start", "end"}}
	ruleShape     = &shape{
		fields: []string{"id", "description", "clauses", "value", "rollout", "schedule"},
		nested: map[string]*shape{"clauses": clauseShape, "rollout": rolloutShape, "schedule": scheduleShape},
	}
	flagShape    = &shape{fields: []string{"type", "value", "targeting"}, nested: map[string]*shape{"targeting": ruleShape}}
	chamberShape = &shape{fields: []string{"rules"}, nested: map[string]*shape{"rules": {each: flagShape}}}
)

// checkFields reports the first field of the JSON value b that s does not
// allow. b has already been decoded successfully, so it is well formed. Field
// names match case-insensitively, as encoding/json matches them.
func checkFields(b []byte, s *shape, at string) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := checkFields(item, s, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
		return nil
	}
	if b[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for name, v := range m {
		where := name
		if at != "" {
			where = at + "." + name
		}
		if s.each != nil {
			if err := checkFields(v, s.each, where); err != nil {
				return err
			}
			continue
		}
		i := slices.IndexFunc(s.fields, func(f string) bool { return strings.EqualFold(f, name) })
		if i < 0 {
			return fmt.Errorf("unknown field %q", where)
		}
		if n := s.nested[s.fields[i]]; n != nil {
			if err := checkFields(v, n, where); err != nil {
				return err
			}
		}
	}
	return nil
}
