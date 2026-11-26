package realm

import (
	"sync"

	"github.com/steviebps/realm/internal/evaluations"
)

// DefaultRuleName is the rule realm.flag.evaluations reports when a flag
// serves its default value: no targeting rule applied, or its targeting is
// turned off.
const DefaultRuleName = evaluations.DefaultRule

// ruleNameAt names the targeting rule at index rule of Targeting, as
// realm.flag.evaluations reports it: its id, or its position ("#2") when it
// has none. An index outside Targeting (resolve's -1) names the default.
func (f *Flag) ruleNameAt(rule int) string {
	if rule < 0 || rule >= len(f.Targeting) {
		return DefaultRuleName
	}
	return ruleName(f.Targeting[rule].ID, rule)
}

// viewEvaluations counts the values a view's flags serve, at the view's
// path, in realm.flag.evaluations. It finds the series of a flag's rules on
// the flag's first evaluation, so that counting one after that only adds to
// an atomic counter, without allocating.
type viewEvaluations struct {
	path string
	// flags maps each flag evaluated so far to the series of each of its
	// targeting rules, then of its default (a *[]*evaluations.Series).
	flags sync.Map
}

// count counts a value of the flag f, held at key, that the targeting rule
// at index rule served, or its default when rule is -1. A nil
// viewEvaluations counts nothing.
func (v *viewEvaluations) count(key string, f *Flag, rule int) {
	if v == nil {
		return
	}
	found, ok := v.flags.Load(key)
	if !ok {
		found = v.add(key, f)
	}
	series := *found.(*[]*evaluations.Series)
	if rule < 0 || rule >= len(series)-1 {
		rule = len(series) - 1
	}
	series[rule].Add()
}

// add finds the series of the flag f, held at key, and records them. It is
// apart from count so that count's own variables stay off the heap.
func (v *viewEvaluations) add(key string, f *Flag) any {
	series := make([]*evaluations.Series, len(f.Targeting)+1)
	for i := range series {
		series[i] = evaluations.Of(v.path, key, f.ruleNameAt(i))
	}
	found, _ := v.flags.LoadOrStore(key, &series)
	return found
}
