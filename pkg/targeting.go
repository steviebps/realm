package realm

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
		if !semver.IsValid(canonicalSemver(c.Values[0])) {
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
		for _, want := range c.Values {
			if v == want {
				return true
			}
		}
	case OpNotIn:
		for _, want := range c.Values {
			if v == want {
				return false
			}
		}
		return true
	case OpStartsWith:
		for _, want := range c.Values {
			if strings.HasPrefix(v, want) {
				return true
			}
		}
	case OpSemverGte, OpSemverLte:
		have := canonicalSemver(v)
		if !semver.IsValid(have) {
			return false
		}
		cmp := semver.Compare(have, canonicalSemver(c.Values[0]))
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
	sum := sha1.Sum([]byte(seed + "." + key))
	// Use the 15 leading hex digits (60 bits) of the digest, matching the
	// widely used LaunchDarkly-style bucketing algorithm. Shifting the first
	// 8 bytes right by 4 yields exactly those 60 bits without allocating.
	val := binary.BigEndian.Uint64(sum[:8]) >> 4
	return (float64(val) / longScale) * 100
}

// TargetingRule serves Value to contexts that match every Clause and, when a
// Rollout is set, fall within it. Rules are evaluated in order and the first
// one that applies wins.
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
}

// Applies reports whether the rule should serve its value for the context.
func (tr *TargetingRule) Applies(flagKey string, ec EvaluationContext) bool {
	for _, c := range tr.Clauses {
		if !c.Matches(ec) {
			return false
		}
	}
	return tr.Rollout.Includes(flagKey, ec)
}

// targetingRuleJSON mirrors TargetingRule with a raw value so the value can be
// type-checked against the owning flag's type.
type targetingRuleJSON struct {
	ID          string          `json:"id,omitempty"`
	Description string          `json:"description,omitempty"`
	Clauses     []*Clause       `json:"clauses,omitempty"`
	Value       json.RawMessage `json:"value"`
	Rollout     *Rollout        `json:"rollout,omitempty"`
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
		v := &Rule{Type: ruleType}
		if err := v.assertType(r.Value); err != nil {
			return nil, fmt.Errorf("targeting rule %s: %q of the specified type %q is incompatible: %w", name, string(r.Value), ruleType, err)
		}
		value := v.Value
		if ruleType == "custom" {
			raw := r.Value
			value = &raw
		}
		rules = append(rules, &TargetingRule{
			ID:          r.ID,
			Description: r.Description,
			Clauses:     r.Clauses,
			Value:       value,
			Rollout:     r.Rollout,
		})
	}
	return rules, nil
}
