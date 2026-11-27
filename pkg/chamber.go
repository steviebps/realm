package realm

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"strconv"
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

	// rule is unexported, so no encoding of an evaluation sent to a browser
	// can reveal it.
	rule string
}

// Rule names the targeting rule that served the flag's value, as
// realm.flag.evaluations reports it: its id, its position ("#2") when it has
// none, or DefaultRuleName. It is "" for an EvaluatedFlag that
// Chamber.Evaluate did not make.
func (e EvaluatedFlag) Rule() string {
	return e.rule
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
		value, rule := f.resolve(key, ec)
		flags[key] = EvaluatedFlag{Type: f.Type, Value: value, rule: f.ruleNameAt(rule)}
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
	// path is the canonical path of the chamber the view belongs to, in a
	// subtree snapshot (see WithPathTemplate); empty otherwise.
	path string
	// evaluations counts the flags the view serves; nil for a view not made
	// by a Realm, which counts nothing.
	evaluations *viewEvaluations
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
// attributes) as a T, wrapping lookup and conversion failures in the
// chamber's error types, and counts the value when it serves one.
func entryValue[T any](c *ChamberEntry, ruleKey string, ec EvaluationContext, defaultValue T) (T, error) {
	t := c.Get(ruleKey)
	if t == nil {
		return defaultValue, &ErrRuleNotFound{Key: ruleKey}
	}
	value, rule := t.resolve(ruleKey, c.context(ec))
	v, ok := value.(T)
	if !ok {
		return defaultValue, &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	c.evaluations.count(ruleKey, t, rule)
	return v, nil
}

// StringValueFor retrieves a string by the key of the rule, resolved for the
// given evaluation context (applying any targeting rules). It returns the
// default value and an error if the rule is not found or could not be converted.
func (c *ChamberEntry) StringValueFor(ruleKey string, ec EvaluationContext, defaultValue string) (string, error) {
	return entryValue(c, ruleKey, ec, defaultValue)
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
	return entryValue(c, ruleKey, ec, defaultValue)
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
	return entryValue(c, ruleKey, ec, defaultValue)
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
	rule, err := t.customValue(ruleKey, c.context(ec), v)
	if err != nil {
		return &ErrCouldNotConvertRule{Key: ruleKey, Type: t.Type}
	}
	c.evaluations.count(ruleKey, t, rule)
	return nil
}

// CustomValue retrieves a json.RawMessage by the key of the rule
// and returns an error if it is not found or could not be converted
func (c *ChamberEntry) CustomValue(ruleKey string, v any) error {
	return c.CustomValueFor(ruleKey, EvaluationContext{}, v)
}

// ParseChamber decodes and validates a chamber strictly, as the server does
// for writes. It is json.Unmarshal into a Chamber, except that a field,
// operator, flag type, lifecycle, or constraint keyword realm does not know,
// at any level, is an error instead of being ignored: a misspelled "schedule"
// would otherwise make its rule apply immediately, a misspelled "rules" would
// replace the chamber with an empty one, and a misspelled operator would
// never match. So is a name repeated within an object, anywhere, which
// json.Unmarshal resolves by keeping the last: a flag key written twice would
// otherwise drop the first definition, and a second "schedule" the first's
// bounds. Realm's own fields repeat in any case ("type" and "Type"). It also
// refuses a flag with rules in both targeting and pausedTargeting, a
// targeting rule id that realm.flag.evaluations reserves ("default", or one
// starting with "#"), and a flag whose default value or targeting rule
// values, paused or not, its constraints refuse. (Reads decode leniently, so
// a newer server's additions do not break older SDKs, and a stored value
// outside its constraints does not stop a chamber from being served.)
func ParseChamber(b []byte) (*Chamber, error) {
	var c Chamber
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if err := checkFields(b, chamberShape); err != nil {
		return nil, err
	}
	if err := c.checkDefined(); err != nil {
		return nil, err
	}
	if err := c.checkSupported(); err != nil {
		return nil, err
	}
	if err := c.checkPaused(); err != nil {
		return nil, err
	}
	if err := c.checkConstraints(); err != nil {
		return nil, err
	}
	return &c, nil
}

// checkDefined reports the first flag, in key order, written as null: it
// serves nothing, so a write that means to remove a flag must leave it out.
// Reads accept one, which SDKs skip.
func (c *Chamber) checkDefined() error {
	for _, key := range slices.Sorted(maps.Keys(c.Rules)) {
		if c.Rules[key] == nil {
			return fmt.Errorf("flag %q has no definition; leave it out to remove it", key)
		}
	}
	return nil
}

// checkConstraints reports the first flag, in key order, with a lifecycle
// this version of realm does not know, constraints it cannot parse, or a
// value its constraints refuse.
func (c *Chamber) checkConstraints() error {
	for _, key := range slices.Sorted(maps.Keys(c.Rules)) {
		f := c.Rules[key]
		if f == nil || f.Rule == nil {
			continue
		}
		if !f.Lifecycle.known() {
			return fmt.Errorf("flag %q: lifecycle must be %q or %q, not %q", key, LifecycleTemporary, LifecyclePermanent, f.Lifecycle)
		}
		s, err := f.ParseConstraints()
		if err != nil {
			return fmt.Errorf("flag %q: constraints: %w", key, err)
		}
		if err := f.CheckValues(s); err != nil {
			return fmt.Errorf("flag %q: %w", key, err)
		}
	}
	return nil
}

// checkSupported reports the first flag, in key order, of a type or with an
// operator this version of realm does not know, or with a targeting rule,
// paused or not, whose id realm.flag.evaluations reserves: DefaultRuleName,
// or one starting with "#", which names a rule without an id by its
// position.
func (c *Chamber) checkSupported() error {
	for _, key := range slices.Sorted(maps.Keys(c.Rules)) {
		f := c.Rules[key]
		if f == nil || f.Rule == nil {
			continue
		}
		if !knownType(f.Type) {
			return fmt.Errorf("flag %q: %w", key, &UnsupportedTypeError{RuleType: f.Type})
		}
		for _, rules := range []struct {
			name  string
			rules []*TargetingRule
		}{{"targeting rule", f.Targeting}, {"paused targeting rule", f.PausedTargeting}} {
			for i, tr := range rules.rules {
				switch {
				case tr.ID == DefaultRuleName:
					return fmt.Errorf("flag %q: %s id %q is reserved: it names the default value in realm.flag.evaluations", key, rules.name, tr.ID)
				case strings.HasPrefix(tr.ID, "#"):
					return fmt.Errorf("flag %q: %s id %q is reserved: ids starting with \"#\" name rules without one in realm.flag.evaluations", key, rules.name, tr.ID)
				}
				for _, cl := range tr.Clauses {
					if !cl.Op.known() {
						return fmt.Errorf("flag %q: %s %s: clause on %q: unsupported operator %q", key, rules.name, ruleName(tr.ID, i), cl.Attribute, cl.Op)
					}
				}
			}
		}
	}
	return nil
}

// checkPaused reports the first flag, in key order, with rules in both
// targeting and pausedTargeting: a flag's targeting is either on or off.
func (c *Chamber) checkPaused() error {
	for _, key := range slices.Sorted(maps.Keys(c.Rules)) {
		f := c.Rules[key]
		if f != nil && len(f.Targeting) > 0 && len(f.PausedTargeting) > 0 {
			return fmt.Errorf("flag %q has rules in both targeting and pausedTargeting: its targeting is either on (rules in targeting) or off (rules in pausedTargeting)", key)
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
	flagShape = &shape{
		fields: []string{"type", "value", "description", "owner", "lifecycle", "constraints", "sealed", "targeting", "pausedTargeting"},
		nested: map[string]*shape{"targeting": ruleShape, "pausedTargeting": ruleShape},
	}
	chamberShape = &shape{fields: []string{"rules"}, nested: map[string]*shape{"rules": {each: flagShape}}}
)

// checkFields reports the first field, in document order, of the JSON value
// b that s does not allow, or that repeats a name in its object. b has
// already been decoded successfully, so it is well formed. Field names match
// case-insensitively, as encoding/json matches them, so "type" and "Type"
// in a flag repeat a name; other names (flag keys, and those in custom
// values and constraints, which s does not describe) repeat only when equal.
// It reads b once, as a stream of tokens, copying no part of it.
func checkFields(b []byte, s *shape) error {
	// read as json.Unmarshal reads, with duplicate names (which checkValueFields
	// reports itself, with their path) and invalid UTF-8 allowed
	dec := jsontext.NewDecoder(bytes.NewReader(b), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	return checkValueFields(dec, s)
}

// checkValueFields checks the next value dec holds against s, and consumes
// it. A nil s allows any field, and only names repeated exactly are refused.
func checkValueFields(dec *jsontext.Decoder, s *shape) error {
	switch dec.PeekKind() {
	case '[':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		for i := 0; dec.PeekKind() != ']'; i++ {
			if err := checkValueFields(dec, s); err != nil {
				return within(err, pathStep{index: i})
			}
		}
		_, err := dec.ReadToken()
		return err
	case '{':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		var seen names
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return err
			}
			name := tok.String()
			// key is the name as encoding/json resolves it: a field of s
			// whatever its case, else the name itself
			key, next := name, (*shape)(nil)
			switch {
			case s == nil:
			case s.each != nil:
				next = s.each
			default:
				i := slices.IndexFunc(s.fields, func(f string) bool { return strings.EqualFold(f, name) })
				if i < 0 {
					return &fieldError{problem: "unknown", path: []pathStep{{name: name, index: -1}}}
				}
				key, next = s.fields[i], s.nested[s.fields[i]]
			}
			if !seen.add(key) {
				return &fieldError{problem: "duplicate", path: []pathStep{{name: name, index: -1}}}
			}
			if err := checkValueFields(dec, next); err != nil {
				return within(err, pathStep{name: name, index: -1})
			}
		}
		_, err := dec.ReadToken()
		return err
	}
	return dec.SkipValue()
}

// names records the member names of one object, to find a repeated one: in
// a fixed list while there are few, as there are in most objects, then in a
// set.
type names struct {
	few [16]string
	n   int
	set map[string]struct{}
}

// add records name, and reports whether it was new.
func (n *names) add(name string) bool {
	if n.set == nil {
		if slices.Contains(n.few[:n.n], name) {
			return false
		}
		if n.n < len(n.few) {
			n.few[n.n] = name
			n.n++
			return true
		}
		n.set = make(map[string]struct{}, 2*len(n.few))
		for _, f := range n.few {
			n.set[f] = struct{}{}
		}
	}
	if _, ok := n.set[name]; ok {
		return false
	}
	n.set[name] = struct{}{}
	return true
}

// fieldError is a field checkFields refuses.
type fieldError struct {
	// problem says what is wrong with it: "unknown" or "duplicate".
	problem string
	// path leads to the field, innermost step first, as it is built while
	// the walk unwinds.
	path []pathStep
}

// pathStep is a step of a fieldError's path: an array index when index >= 0,
// else an object member's name (which may be empty).
type pathStep struct {
	name  string
	index int
}

// within adds step to the path of err when err is a fieldError.
func within(err error, step pathStep) error {
	if e, ok := err.(*fieldError); ok {
		e.path = append(e.path, step)
	}
	return err
}

func (e *fieldError) Error() string {
	var where strings.Builder
	for _, step := range slices.Backward(e.path) {
		switch {
		case step.index >= 0:
			where.WriteString("[" + strconv.Itoa(step.index) + "]")
		case where.Len() > 0:
			where.WriteString("." + step.name)
		default:
			where.WriteString(step.name)
		}
	}
	return fmt.Sprintf("%s field %q", e.problem, where.String())
}
