package realm

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// longScale is the maximum value representable by the 15 leading hex digits
// (60 bits) of the bucketing hash. It normalizes the hash into the [0,1) range.
const longScale = float64(0xFFFFFFFFFFFFFFF)

// Rollout serves an alternate value to a deterministic percentage of evaluation
// contexts, bucketed by the context Key. It embeds *Rule so the rollout value
// is validated against the rule's declared type, exactly like Override.
//
// A rollout is how a feature is progressively enabled for a subset of a
// population (for example, "on for 20% of users") independently of the
// application version.
type Rollout struct {
	*Rule
	// Percentage is the portion of evaluation contexts, in the range [0,100],
	// that receive the rollout Value. The rest fall through to the rule's base
	// (or version-overridden) value.
	Percentage float64 `json:"percentage"`
	// Seed makes bucketing independent between rollouts. When empty, the rule
	// key is used, so each rule buckets independently by default. Set a shared
	// Seed across rules to make them roll out to the same population together.
	Seed string `json:"seed,omitempty"`
}

// UnmarshalJSON validates the rollout value against its declared type and
// ensures the percentage is within [0,100].
func (r *Rollout) UnmarshalJSON(b []byte) error {
	var rule Rule
	if err := json.Unmarshal(b, &rule); err != nil {
		return err
	}
	r.Rule = &rule

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	if v, ok := m["percentage"]; ok {
		if err := json.Unmarshal(v, &r.Percentage); err != nil {
			return err
		}
	}
	if v, ok := m["seed"]; ok {
		if err := json.Unmarshal(v, &r.Seed); err != nil {
			return err
		}
	}

	if r.Value == nil {
		return errors.New("rollout value cannot be empty/nil")
	}

	if r.Percentage < 0 || r.Percentage > 100 {
		return fmt.Errorf("rollout percentage %v is not within the range [0,100]", r.Percentage)
	}

	return nil
}

// bucket returns a stable value in the range [0,100) for the given seed and
// key. The same (seed, key) pair always maps to the same bucket, which is what
// makes rollout assignment sticky for a given evaluation context.
func bucket(seed, key string) float64 {
	sum := sha1.Sum([]byte(seed + "." + key))
	// Use the 15 leading hex digits (60 bits) of the digest, matching the
	// widely used LaunchDarkly-style bucketing algorithm.
	val, _ := strconv.ParseInt(hex.EncodeToString(sum[:])[:15], 16, 64)
	return (float64(val) / longScale) * 100
}

// Includes reports whether the evaluation context falls within the rollout.
//
// A nil rollout or a context with an empty Key is never included, so a rollout
// is inert until the caller supplies a bucketing key. ruleKey is used as the
// default bucketing seed when Seed is empty.
func (r *Rollout) Includes(ruleKey string, ec EvaluationContext) bool {
	if r == nil || ec.Key == "" {
		return false
	}
	seed := r.Seed
	if seed == "" {
		seed = ruleKey
	}
	return bucket(seed, ec.Key) < r.Percentage
}
