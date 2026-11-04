package realm

import "time"

// KeyAttribute is the attribute name under which EvaluationContext.Key is
// exposed to targeting clauses and rollout bucketing.
const KeyAttribute = "key"

// VersionAttribute is the attribute name set by WithVersion and matched by the
// semver operators by convention.
const VersionAttribute = "version"

// EvaluationContext carries per-evaluation targeting information used to
// resolve a flag for a specific subject (for example, a user, a device, or a
// location).
//
// It is passed through the request context (see Realm.NewContextWithEvaluation)
// so that every flag evaluated during a single logical request sees the same
// context and therefore resolves consistently.
type EvaluationContext struct {
	// Key identifies the subject (for example, a user id). It is exposed to
	// clauses as the "key" attribute and is the default rollout bucketing
	// attribute. The same Key always buckets to the same value, which makes
	// rollout assignment sticky.
	Key string

	// Attributes carries targeting data keyed by attribute name, such as
	// "tenantId", "locationId", or "environment". Clauses on a flag's
	// targeting rules are matched against these values.
	Attributes map[string]string

	// defaults holds process-wide attributes (see WithAttributes) consulted
	// when an attribute is absent from Attributes.
	defaults map[string]string

	// at fixes the time schedules are evaluated at; when zero, the current
	// time is used.
	at time.Time

	// version caches the canonical semver form of the process-wide version.
	version semverCache
}

// semverCache pairs a version as given with its canonical ("v"-prefixed)
// form.
type semverCache struct {
	raw, canonical string
}

// canonicalVersion returns the canonical semver form of v, without
// allocating when v is the cached process-wide version.
func (ec EvaluationContext) canonicalVersion(v string) string {
	if v != "" && v == ec.version.raw {
		return ec.version.canonical
	}
	return canonicalSemver(v)
}

// Attribute returns the value of the named attribute and whether it is present.
// The "key" attribute resolves to Key; other names resolve to Attributes first
// and then to any process-wide attributes configured on the Realm.
func (ec EvaluationContext) Attribute(name string) (string, bool) {
	if name == KeyAttribute && ec.Key != "" {
		return ec.Key, true
	}
	if v, ok := ec.Attributes[name]; ok {
		return v, true
	}
	v, ok := ec.defaults[name]
	return v, ok
}

// now returns the time schedules are evaluated at.
func (ec EvaluationContext) now() time.Time {
	if !ec.at.IsZero() {
		return ec.at
	}
	return time.Now()
}

// withDefaults returns a copy of ec that falls back to the provided attributes.
func (ec EvaluationContext) withDefaults(defaults map[string]string) EvaluationContext {
	ec.defaults = defaults
	return ec
}
