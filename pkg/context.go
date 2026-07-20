package realm

// EvaluationContext carries per-evaluation targeting information used to
// resolve a rule for a specific subject (for example, a user or a device).
//
// It is passed through the request context (see Realm.NewContextWithEvaluation)
// so that every rule evaluated during a single logical request sees the same
// context and therefore resolves consistently.
type EvaluationContext struct {
	// Key is the deterministic bucketing key (for example, a user id) used by
	// percentage rollouts. The same Key always buckets to the same value, which
	// makes rollout assignment sticky. An empty Key disables rollout targeting.
	Key string

	// Attributes carries additional targeting data keyed by attribute name. It
	// is reserved for future attribute-based targeting and is not yet consulted
	// during evaluation.
	Attributes map[string]any
}
