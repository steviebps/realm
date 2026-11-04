package realm

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Operator is the comparison a Clause applies between a context attribute and
// its values.
type Operator string

const (
	// OpIn matches when the attribute equals any of the values.
	OpIn Operator = "in"
	// OpNotIn matches when the attribute is present and equals none of the values.
	OpNotIn Operator = "notIn"
	// OpStartsWith matches when the attribute has any of the values as a prefix
	// (for example, a short build SHA).
	OpStartsWith Operator = "startsWith"
	// OpSemverGte matches when the attribute is a semantic version greater than
	// or equal to the single value.
	OpSemverGte Operator = "semverGte"
	// OpSemverLte matches when the attribute is a semantic version less than or
	// equal to the single value.
	OpSemverLte Operator = "semverLte"
)

// longScale is the maximum value representable by the 15 leading hex digits
// (60 bits) of the bucketing hash. It normalizes the hash into the [0,1) range.
const longScale = float64(0xFFFFFFFFFFFFFFF)

// Clause is a single condition of a TargetingRule, matched against one
// attribute of the EvaluationContext.
type Clause struct {
	Attribute string   `json:"attribute"`
	Op        Operator `json:"op"`
	Values    []string `json:"values"`

	// semverBound caches the "v"-prefixed semver operand so evaluation does
	// not rebuild it. Set by validation; computed on demand when empty.
	semverBound string
}

type clauseAlias Clause

// UnmarshalJSON validates that the clause names an attribute, uses a known
// operator, and carries values appropriate for that operator.
func (c *Clause) UnmarshalJSON(b []byte) error {
	var alias clauseAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*c = Clause(alias)
	return c.validate()
}

func (c *Clause) validate() error {
	if c.Attribute == "" {
		return errors.New("clause attribute cannot be empty")
	}
	if len(c.Values) == 0 {
		return fmt.Errorf("clause on %q must have at least one value", c.Attribute)
	}
	switch c.Op {
	case OpIn, OpNotIn, OpStartsWith:
		return nil
	case OpSemverGte, OpSemverLte:
		if len(c.Values) != 1 {
			return fmt.Errorf("clause on %q: operator %q takes exactly one value", c.Attribute, c.Op)
		}
		c.semverBound = canonicalSemver(c.Values[0])
		if !semver.IsValid(c.semverBound) {
			return fmt.Errorf("clause on %q: %q is not a valid semantic version", c.Attribute, c.Values[0])
		}
		return nil
	}
	return fmt.Errorf("clause on %q: unsupported operator %q", c.Attribute, c.Op)
}

// Matches reports whether the clause holds for the evaluation context. A
// clause never matches when its attribute is absent from the context.
func (c *Clause) Matches(ec EvaluationContext) bool {
	v, ok := ec.Attribute(c.Attribute)
	if !ok {
		return false
	}
	switch c.Op {
	case OpIn:
		return slices.Contains(c.Values, v)
	case OpNotIn:
		return !slices.Contains(c.Values, v)
	case OpStartsWith:
		for _, want := range c.Values {
			if strings.HasPrefix(v, want) {
				return true
			}
		}
	case OpSemverGte, OpSemverLte:
		have := ec.canonicalVersion(v)
		if !semver.IsValid(have) {
			return false
		}
		bound := c.semverBound
		if bound == "" {
			bound = canonicalSemver(c.Values[0])
		}
		cmp := semver.Compare(have, bound)
		if c.Op == OpSemverGte {
			return cmp >= 0
		}
		return cmp <= 0
	}
	return false
}

// canonicalSemver prefixes v with "v" when missing so that both "1.2.3" and
// "v1.2.3" are accepted.
func canonicalSemver(v string) string {
	if v == "" || v[0] == 'v' {
		return v
	}
	return "v" + v
}

// Rollout limits a TargetingRule to a deterministic percentage of the contexts
// that match its clauses. Assignment is sticky: the same bucketing attribute
// value always lands in the same bucket.
type Rollout struct {
	// Percentage is the portion of matching contexts, in the range [0,100],
	// that receive the rule's value. The rest continue to the next rule.
	Percentage float64 `json:"percentage"`
	// BucketBy names the attribute used for bucketing. It defaults to "key".
	// Bucketing by a coarser attribute (for example "locationId") rolls out to
	// whole groups at once.
	BucketBy string `json:"bucketBy,omitempty"`
	// Seed makes bucketing independent between rollouts. When empty, the flag
	// key is used, so each flag buckets independently by default. Set a shared
	// Seed across flags to make them roll out to the same population together.
	Seed string `json:"seed,omitempty"`
}

type rolloutAlias Rollout

// UnmarshalJSON ensures the percentage is within [0,100].
func (r *Rollout) UnmarshalJSON(b []byte) error {
	var alias rolloutAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*r = Rollout(alias)
	if r.Percentage < 0 || r.Percentage > 100 {
		return fmt.Errorf("rollout percentage %v is not within the range [0,100]", r.Percentage)
	}
	return nil
}

// Includes reports whether the evaluation context falls within the rollout.
//
// A nil rollout includes every context. A context missing the bucketing
// attribute is never included. flagKey is used as the default bucketing seed
// when Seed is empty.
func (r *Rollout) Includes(flagKey string, ec EvaluationContext) bool {
	if r == nil {
		return true
	}
	by := r.BucketBy
	if by == "" {
		by = KeyAttribute
	}
	v, ok := ec.Attribute(by)
	if !ok || v == "" {
		return false
	}
	seed := r.Seed
	if seed == "" {
		seed = flagKey
	}
	return bucket(seed, v) < r.Percentage
}

// bucket returns a stable value in the range [0,100) for the given seed and
// key. The same (seed, key) pair always maps to the same bucket, which is what
// makes rollout assignment sticky for a given evaluation context.
func bucket(seed, key string) float64 {
	// Hash "<seed>.<key>" from a stack buffer so typical keys never allocate.
	var buf [128]byte
	b := append(append(append(buf[:0], seed...), '.'), key...)
	sum := sha1.Sum(b)
	// Use the 15 leading hex digits (60 bits) of the digest, matching the
	// widely used LaunchDarkly-style bucketing algorithm. Shifting the first
	// 8 bytes right by 4 yields exactly those 60 bits without allocating.
	val := binary.BigEndian.Uint64(sum[:8]) >> 4
	return (float64(val) / longScale) * 100
}

// Schedule limits a TargetingRule to a window of time: the rule applies from
// Start (inclusive) until End (exclusive). Omitting a bound leaves that side of
// the window open. Bounds are RFC 3339 timestamps, so a window is the same
// span of instants for every host, whatever its time zone.
type Schedule struct {
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`
}

type scheduleAlias Schedule

// UnmarshalJSON ensures the schedule has a bound, ends after it starts, and
// uses no more than millisecond precision, which every SDK can represent.
func (s *Schedule) UnmarshalJSON(b []byte) error {
	var alias scheduleAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*s = Schedule(alias)
	if s.Start.IsZero() && s.End.IsZero() {
		return errors.New("schedule must have a start or an end")
	}
	if !s.Start.IsZero() && !s.End.IsZero() && !s.Start.Before(s.End) {
		return fmt.Errorf("schedule end %s must be after its start %s", s.End.Format(time.RFC3339Nano), s.Start.Format(time.RFC3339Nano))
	}
	for _, t := range []time.Time{s.Start, s.End} {
		if t.Nanosecond()%int(time.Millisecond) != 0 {
			return fmt.Errorf("schedule time %s is more precise than a millisecond", t.Format(time.RFC3339Nano))
		}
	}
	return nil
}

// Includes reports whether the evaluation context's time falls within the
// schedule. A nil schedule includes every time.
func (s *Schedule) Includes(ec EvaluationContext) bool {
	if s == nil {
		return true
	}
	now := ec.now()
	if !s.Start.IsZero() && now.Before(s.Start) {
		return false
	}
	return s.End.IsZero() || now.Before(s.End)
}

// TargetingRule serves Value to contexts that match every Clause and, when a
// Rollout is set, fall within it, while its Schedule (if any) is in effect.
// Rules are evaluated in order and the first one that applies wins.
type TargetingRule struct {
	// ID optionally identifies the rule; when set it must be unique within the flag.
	ID string `json:"id,omitempty"`
	// Description is a human-readable note shown in the UI.
	Description string `json:"description,omitempty"`
	// Clauses are ANDed together. A rule without clauses matches every context.
	Clauses []*Clause `json:"clauses,omitempty"`
	// Value is served when the rule applies. It is validated against the type
	// of the owning flag.
	Value interface{} `json:"value"`
	// Rollout optionally limits the rule to a percentage of matching contexts.
	Rollout *Rollout `json:"rollout,omitempty"`
	// Schedule optionally limits the rule to a window of time.
	Schedule *Schedule `json:"schedule,omitempty"`
}

// Applies reports whether the rule should serve its value for the context.
func (tr *TargetingRule) Applies(flagKey string, ec EvaluationContext) bool {
	if !tr.Schedule.Includes(ec) {
		return false
	}
	for _, c := range tr.Clauses {
		if !c.Matches(ec) {
			return false
		}
	}
	return tr.Rollout.Includes(flagKey, ec)
}

type targetingRuleAlias TargetingRule

// targetingRuleJSON decodes a TargetingRule with its value kept raw so the
// value can be type-checked against the owning flag's type. The outer Value
// field shadows the embedded one.
type targetingRuleJSON struct {
	targetingRuleAlias
	Value json.RawMessage `json:"value"`
}

// parseTargeting decodes and validates targeting rules for a flag of the given type.
func parseTargeting(data json.RawMessage, ruleType string) ([]*TargetingRule, error) {
	var raw []targetingRuleJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	ids := make(map[string]struct{}, len(raw))
	rules := make([]*TargetingRule, 0, len(raw))
	for i, r := range raw {
		name := r.ID
		if name == "" {
			name = "#" + strconv.Itoa(i+1)
		}
		if r.ID != "" {
			if _, dup := ids[r.ID]; dup {
				return nil, fmt.Errorf("targeting rule id %q is not unique", r.ID)
			}
			ids[r.ID] = struct{}{}
		}
		if len(r.Value) == 0 {
			return nil, fmt.Errorf("targeting rule %s: value cannot be empty/nil", name)
		}
		// a JSON null decodes to a nil *Clause without running Clause.UnmarshalJSON
		if slices.Contains(r.Clauses, nil) {
			return nil, fmt.Errorf("targeting rule %s: clauses must not be null", name)
		}
		v := &Rule{Type: ruleType}
		if err := v.assertType(r.Value); err != nil {
			return nil, fmt.Errorf("targeting rule %s: %q of the specified type %q is incompatible: %w", name, string(r.Value), ruleType, err)
		}
		value := v.Value
		if ruleType == "custom" {
			raw := r.Value
			value = &raw
		}
		rule := TargetingRule(r.targetingRuleAlias)
		rule.Value = value
		rules = append(rules, &rule)
	}
	return rules, nil
}
