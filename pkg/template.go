package realm

import (
	"errors"
	"fmt"
	"strings"
)

// pathTemplate is a parsed path template (see WithPathTemplate), such as
// "/prod/tenants/{tenantId}/{locationId}/".
type pathTemplate struct {
	// raw is the template as given.
	raw string
	// root is the template's literal prefix up to its first placeholder,
	// canonical ("/prod/tenants/"): the subtree realm reads and watches.
	root string
	// segments are the segments after the root, in order.
	segments []templateSegment
}

// templateSegment is one segment of a template after its root: literal text,
// or a placeholder filled from an attribute.
type templateSegment struct {
	// text is the literal text, or the placeholder's attribute name.
	text        string
	placeholder bool
}

// parsePathTemplate parses and validates a path template: it starts with
// "/" (a missing trailing "/" is added), and each segment between slashes is
// either literal text without braces or a whole-segment placeholder
// "{name}", where name is one or more of A-Z a-z 0-9 _ - and ".". Empty
// segments, partial placeholders, empty names, and the literal segments "."
// and ".." (which do not name a chamber) are invalid.
func parsePathTemplate(s string) (*pathTemplate, error) {
	if !strings.HasPrefix(s, "/") {
		return nil, fmt.Errorf("invalid path template %q: it must start with /", s)
	}
	t := &pathTemplate{raw: s}
	inner := strings.TrimSuffix(s[1:], "/")
	if inner == "" {
		// "/" or "//": only the first is a template
		if s != "/" {
			return nil, fmt.Errorf("invalid path template %q: empty segment", s)
		}
		t.root = "/"
		return t, nil
	}
	root := "/"
	inLiteralPrefix := true
	for seg := range strings.SplitSeq(inner, "/") {
		if seg == "" {
			return nil, fmt.Errorf("invalid path template %q: empty segment", s)
		}
		ts, err := parseSegment(seg)
		if err != nil {
			return nil, fmt.Errorf("invalid path template %q: %w", s, err)
		}
		if inLiteralPrefix && !ts.placeholder {
			root += seg + "/"
			continue
		}
		inLiteralPrefix = false
		t.segments = append(t.segments, ts)
	}
	t.root = root
	return t, nil
}

// parseSegment parses one non-empty template segment.
func parseSegment(seg string) (templateSegment, error) {
	if !strings.ContainsAny(seg, "{}") {
		if seg == "." || seg == ".." {
			return templateSegment{}, fmt.Errorf("segment %q does not name a chamber", seg)
		}
		return templateSegment{text: seg}, nil
	}
	if len(seg) < 2 || seg[0] != '{' || seg[len(seg)-1] != '}' {
		return templateSegment{}, fmt.Errorf("segment %q: a placeholder must be a whole segment, such as {tenantId}", seg)
	}
	name := seg[1 : len(seg)-1]
	if name == "" {
		return templateSegment{}, errors.New("empty placeholder name")
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return templateSegment{}, fmt.Errorf("placeholder %q: a name may hold only A-Z a-z 0-9 _ - and .", seg)
		}
	}
	return templateSegment{text: name, placeholder: true}, nil
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// placeholderValue reports whether v may fill a placeholder: it is not
// empty, holds no "/" or "..", and is not ".".
func placeholderValue(v string) bool {
	return v != "" && v != "." && !strings.Contains(v, "/") && !strings.Contains(v, "..")
}

// resolve returns the view in views of the deepest candidate path ec builds
// from the template (see docs/design/subtree.md), or nil when views holds
// none of them. ec must already fall back to the process-wide attributes.
// It does not allocate for paths up to 256 bytes.
func (t *pathTemplate) resolve(views map[string]*ChamberEntry, ec EvaluationContext) *ChamberEntry {
	best := views[t.root]
	if len(t.segments) == 0 {
		return best
	}
	var stack [256]byte
	path := append(stack[:0], t.root...)
	for _, seg := range t.segments {
		if seg.placeholder {
			v, ok := ec.Attribute(seg.text)
			if !ok || !placeholderValue(v) {
				break
			}
			path = append(path, v...)
		} else {
			path = append(path, seg.text...)
		}
		path = append(path, '/')
		if e, ok := views[string(path)]; ok {
			best = e
		}
	}
	return best
}
