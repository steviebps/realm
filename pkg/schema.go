package realm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is the subset of JSON Schema that realm checks flag values against:
// a flag's Constraints. It has the keywords type, enum, minimum, maximum,
// multipleOf, minLength, maxLength, properties, required,
// additionalProperties (a boolean), and items (one schema). ParseSchema
// refuses every other keyword, so that no one believes a keyword realm does
// not check (such as pattern) is enforced.
//
// As in JSON Schema, a keyword about one JSON type says nothing about values
// of another: minimum holds only numbers to a bound, and properties only
// objects. Unlike JSON Schema, a schema whose type rules out a keyword (a
// "string" with a minimum, say), or whose enum holds a value the rest of it
// refuses, is refused too: it is surely a mistake.
type Schema struct {
	// Type is the JSON type a value must have: "object", "array", "string",
	// "number", "integer" (a number without a fractional part), "boolean", or
	// "null". Empty allows any type.
	Type string `json:"type,omitempty"`
	// Enum lists the only values allowed, decoded as encoding/json decodes
	// into an any; nil allows any value. Values compare as JSON: 1 and 1.0 are
	// equal, and objects compare by their members whatever their order.
	Enum []any `json:"enum,omitempty"`
	// Minimum and Maximum are inclusive bounds on numbers.
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	// MultipleOf, when set, is a positive number every number must be a
	// multiple of: 1 allows only whole numbers. It is checked with a
	// tolerance of a billionth of the quotient, so that 0.3 is a multiple of
	// 0.1 although floating point says otherwise.
	MultipleOf *float64 `json:"multipleOf,omitempty"`
	// MinLength and MaxLength are inclusive bounds on the length of strings,
	// in Unicode code points.
	MinLength *int `json:"minLength,omitempty"`
	MaxLength *int `json:"maxLength,omitempty"`
	// Properties holds the schemas of an object's members, by name.
	Properties map[string]*Schema `json:"properties,omitempty"`
	// Required lists the members every object must have.
	Required []string `json:"required,omitempty"`
	// AdditionalProperties, when false, refuses objects with members that
	// Properties does not name.
	AdditionalProperties *bool `json:"additionalProperties,omitempty"`
	// Items is the schema of every element of an array.
	Items *Schema `json:"items,omitempty"`
}

// schemaKeywords lists the keywords ParseSchema accepts, as its errors name
// them.
const schemaKeywords = "type, enum, minimum, maximum, multipleOf, minLength, maxLength, properties, required, additionalProperties, and items"

// schemaTypes lists the JSON types a schema may name.
var schemaTypes = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

// typeKeywords maps each JSON type to the keywords about values of that type
// only. Keywords about any type (type and enum) are in no list.
var typeKeywords = map[string][]string{
	"number":  {"minimum", "maximum", "multipleOf"},
	"integer": {"minimum", "maximum", "multipleOf"},
	"string":  {"minLength", "maxLength"},
	"object":  {"properties", "required", "additionalProperties"},
	"array":   {"items"},
}

// ParseSchema decodes a schema strictly: every keyword must be one realm
// checks, of the right form, and consistent with the others (see Schema).
// Keywords are matched exactly, as JSON Schema matches them.
func ParseSchema(b []byte) (*Schema, error) {
	return parseSchema(b, "", "")
}

// parseFlagSchema parses the constraints of a flag of type flagType: as
// ParseSchema does, except that for a flag of a scalar type the schema must
// be about values of that type. at locates the schema in errors.
func parseFlagSchema(b []byte, flagType, at string) (*Schema, error) {
	var implied string
	switch flagType {
	case "number", "string", "boolean":
		implied = flagType
	case "custom":
	default:
		return nil, &UnsupportedTypeError{RuleType: flagType}
	}
	return parseSchema(b, at, implied)
}

// parseSchema parses the schema b, found at at. implied is the JSON type the
// values it checks are known to have (empty when unknown): its keywords must
// be about that type, and its type, when set, must agree.
func parseSchema(b []byte, at, implied string) (*Schema, error) {
	fail := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if at == "" {
			return errors.New(msg)
		}
		return fmt.Errorf("%s: %s", at, msg)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, fail("a schema must be a JSON object")
	}

	s := &Schema{}
	for _, kw := range slices.Sorted(maps.Keys(m)) {
		v := m[kw]
		var err error
		switch kw {
		case "type":
			if json.Unmarshal(v, &s.Type) != nil || !slices.Contains(schemaTypes, s.Type) {
				return nil, fail("type must be one of %s", strings.Join(quoteAll(schemaTypes), ", "))
			}
		case "enum":
			if json.Unmarshal(v, &s.Enum) != nil || len(s.Enum) == 0 {
				return nil, fail("enum must be an array of at least one value")
			}
		case "minimum":
			s.Minimum, err = schemaNumber(v)
		case "maximum":
			s.Maximum, err = schemaNumber(v)
		case "multipleOf":
			if s.MultipleOf, err = schemaNumber(v); err == nil && *s.MultipleOf <= 0 {
				err = errors.New("must be greater than 0")
			}
		case "minLength":
			s.MinLength, err = schemaLength(v)
		case "maxLength":
			s.MaxLength, err = schemaLength(v)
		case "properties":
			var props map[string]json.RawMessage
			if json.Unmarshal(v, &props) != nil || props == nil {
				return nil, fail("properties must be an object of schemas")
			}
			s.Properties = make(map[string]*Schema, len(props))
			for _, name := range slices.Sorted(maps.Keys(props)) {
				p, err := parseSchema(props[name], joinPath(joinPath(at, "properties"), name), "")
				if err != nil {
					return nil, err
				}
				s.Properties[name] = p
			}
		case "required":
			if json.Unmarshal(v, &s.Required) != nil || s.Required == nil {
				return nil, fail("required must be an array of property names")
			}
			for i, name := range s.Required {
				if slices.Index(s.Required, name) != i {
					return nil, fail("required names %q twice", name)
				}
			}
		case "additionalProperties":
			var allowed bool
			if json.Unmarshal(v, &allowed) != nil || isNull(v) {
				return nil, fail("additionalProperties must be true or false (realm does not check a schema there)")
			}
			s.AdditionalProperties = &allowed
		case "items":
			if bytes.HasPrefix(bytes.TrimSpace(v), []byte("[")) {
				return nil, fail("items must be one schema (realm does not check an array of them)")
			}
			if s.Items, err = parseSchema(v, joinPath(at, "items"), ""); err != nil {
				return nil, err
			}
		default:
			return nil, fail("unknown keyword %q: realm checks only %s", kw, schemaKeywords)
		}
		if err != nil {
			return nil, fail("%s %v", kw, err)
		}
	}

	if implied != "" && s.Type != "" && s.Type != implied && (implied != "number" || s.Type != "integer") {
		return nil, fail("type %q does not match the flag's type %q", s.Type, implied)
	}
	effective := s.Type
	if effective == "" {
		effective = implied
	}
	if effective != "" {
		applies := typeKeywords[effective]
		for _, kw := range slices.Sorted(maps.Keys(m)) {
			if kw == "type" || kw == "enum" || slices.Contains(applies, kw) {
				continue
			}
			if s.Type != "" {
				return nil, fail("%s does not apply to type %q", kw, s.Type)
			}
			return nil, fail("%s does not apply to a %s flag", kw, implied)
		}
	}
	if s.Minimum != nil && s.Maximum != nil && *s.Minimum > *s.Maximum {
		return nil, fail("minimum %s is greater than maximum %s", formatNumber(*s.Minimum), formatNumber(*s.Maximum))
	}
	if s.MinLength != nil && s.MaxLength != nil && *s.MinLength > *s.MaxLength {
		return nil, fail("minLength %d is greater than maxLength %d", *s.MinLength, *s.MaxLength)
	}
	if s.AdditionalProperties != nil && !*s.AdditionalProperties {
		for _, name := range s.Required {
			if _, ok := s.Properties[name]; !ok {
				return nil, fail("required property %q is not in properties, and additionalProperties is false", name)
			}
		}
	}
	// every value of the enum must be one the rest of the schema allows
	if s.Enum != nil {
		rest := *s
		rest.Enum = nil
		rest.Type = effective
		for _, e := range s.Enum {
			if err := rest.Validate(e); err != nil {
				return nil, fail("enum value %s is never allowed: %v", describeValue(e), err)
			}
		}
	}
	return s, nil
}

// schemaNumber decodes a keyword's number.
func schemaNumber(v json.RawMessage) (*float64, error) {
	var n float64
	if json.Unmarshal(v, &n) != nil || isNull(v) {
		return nil, errors.New("must be a number")
	}
	return &n, nil
}

// schemaLength decodes a keyword's non-negative integer.
func schemaLength(v json.RawMessage) (*int, error) {
	n, err := schemaNumber(v)
	if err != nil || *n < 0 || *n != math.Trunc(*n) || *n > math.MaxInt32 {
		return nil, errors.New("must be a non-negative integer")
	}
	i := int(*n)
	return &i, nil
}

// isNull reports whether the JSON value v is null.
func isNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// ValueError reports a value that a Schema refuses.
type ValueError struct {
	// Path locates the refused value within the value checked, as members
	// and indexes ("items[2].qty"); empty for the value itself.
	Path string
	// Reason says why it is refused.
	Reason string
}

func (e *ValueError) Error() string {
	if e.Path == "" {
		return e.Reason
	}
	return e.Path + ": " + e.Reason
}

// Validate reports the first part of v, a value as encoding/json decodes it
// into an any, that s refuses, as a *ValueError; nil when s allows v.
func (s *Schema) Validate(v any) error {
	return s.validate(v, "")
}

func (s *Schema) validate(v any, at string) error {
	fail := func(format string, args ...any) error {
		return &ValueError{Path: at, Reason: fmt.Sprintf(format, args...)}
	}
	if s.Type != "" && !hasType(v, s.Type) {
		return fail("%s is not %s", describeValue(v), withArticle(s.Type))
	}
	if s.Enum != nil && !slices.ContainsFunc(s.Enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		allowed := make([]string, len(s.Enum))
		for i, e := range s.Enum {
			allowed[i] = describeValue(e)
		}
		return fail("%s is not one of the allowed values: %s", describeValue(v), strings.Join(allowed, ", "))
	}
	switch v := v.(type) {
	case float64:
		if s.Minimum != nil && v < *s.Minimum {
			return fail("%s is less than the minimum, %s", formatNumber(v), formatNumber(*s.Minimum))
		}
		if s.Maximum != nil && v > *s.Maximum {
			return fail("%s is greater than the maximum, %s", formatNumber(v), formatNumber(*s.Maximum))
		}
		if s.MultipleOf != nil && !isMultiple(v, *s.MultipleOf) {
			return fail("%s is not a multiple of %s", formatNumber(v), formatNumber(*s.MultipleOf))
		}
	case string:
		n := utf8.RuneCountInString(v)
		if s.MinLength != nil && n < *s.MinLength {
			return fail("%s is shorter than the minimum length, %d (it has %d characters)", describeValue(v), *s.MinLength, n)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return fail("%s is longer than the maximum length, %d (it has %d characters)", describeValue(v), *s.MaxLength, n)
		}
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := v[name]; !ok {
				return fail("required property %q is missing", name)
			}
		}
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if p, ok := s.Properties[name]; ok {
				if err := p.validate(v[name], joinPath(at, name)); err != nil {
					return err
				}
			} else if s.AdditionalProperties != nil && !*s.AdditionalProperties {
				return fail("property %q is not allowed", name)
			}
		}
	case []any:
		if s.Items != nil {
			for i, e := range v {
				if err := s.Items.validate(e, at+"["+strconv.Itoa(i)+"]"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// hasType reports whether v, as encoding/json decodes into an any, has the
// JSON type t.
func hasType(v any, t string) bool {
	switch v := v.(type) {
	case nil:
		return t == "null"
	case bool:
		return t == "boolean"
	case string:
		return t == "string"
	case float64:
		return t == "number" || t == "integer" && v == math.Trunc(v) && !math.IsInf(v, 0)
	case map[string]any:
		return t == "object"
	case []any:
		return t == "array"
	}
	return false
}

// isMultiple reports whether v is a multiple of m (m > 0), within a tolerance
// of a billionth of the quotient for floating point's rounding.
func isMultiple(v, m float64) bool {
	q := v / m
	if math.IsInf(q, 0) || math.IsNaN(q) {
		return false
	}
	return math.Abs(q-math.Round(q)) <= 1e-9*math.Max(1, math.Abs(q))
}

// withArticle names the JSON type t with an indefinite article.
func withArticle(t string) string {
	switch t {
	case "object", "array", "integer":
		return "an " + t
	case "null":
		return "null"
	}
	return "a " + t
}

// formatNumber formats n as JSON would.
func formatNumber(n float64) string {
	return strconv.FormatFloat(n, 'g', -1, 64)
}

// maxDescribed is the length past which describeValue shortens a value.
const maxDescribed = 60

// describeValue writes v as JSON for an error, shortened when long.
func describeValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if utf8.RuneCount(b) <= maxDescribed {
		return string(b)
	}
	r := []rune(string(b))
	return string(r[:maxDescribed-1]) + "…"
}

// joinPath appends name to the path at, as a member: dotted when name is a
// plain identifier, quoted in brackets otherwise.
func joinPath(at, name string) string {
	switch {
	case !isIdentifier(name):
		return at + "[" + strconv.Quote(name) + "]"
	case at == "":
		return name
	}
	return at + "." + name
}

// isIdentifier reports whether name is a JavaScript-style identifier of
// ASCII letters, digits, "_", and "$" that does not start with a digit.
func isIdentifier(name string) bool {
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for _, r := range name {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && r != '_' && r != '$' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// quoteAll quotes each of names.
func quoteAll(names []string) []string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return quoted
}
