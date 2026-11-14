package realm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// maxSubtreeBytes bounds a subtree read (see WithPathTemplate). The server
// caps a subtree at 10,000 chambers by default, so this allows views of
// about 100 KiB each on average. The response is decoded as it is read, one
// view at a time, so it is never held whole in memory.
const maxSubtreeBytes = 1 << 30

// errNotSubtree reports an answer to a subtree read or watch that is not a
// subtree: the server likely predates subtree reads.
var errNotSubtree = errors.New("not a subtree; the realm server may predate subtree reads (?subtree=true): upgrade it, or use WithPath")

// errTooLarge reports a subtree read larger than maxSubtreeBytes.
var errTooLarge = fmt.Errorf("the response is larger than %d bytes", maxSubtreeBytes)

// subtreeEntry is one view of a subtree read, or the data of a subtree
// stream's chamber or deleted event, as the server sends it.
type subtreeEntry struct {
	Path     string          `json:"path"`
	Rules    json.RawMessage `json:"rules"`
	Revision json.RawMessage `json:"revision"`
	Error    string          `json:"error"`
}

// subtreeView is a decoded subtreeEntry.
type subtreeView struct {
	// path is the view's canonical path.
	path string
	// chamber is the view, or nil when it could not be built (see err).
	chamber  *Chamber
	revision uint64
	// err says why the view could not be built.
	err string
}

// decode decodes e, which must name a path. A view without rules, or with
// rules that cannot be read, is one that could not be built.
func (e *subtreeEntry) decode() (subtreeView, error) {
	if e.Path == "" {
		return subtreeView{}, fmt.Errorf(`%w: an entry has no "path"`, errNotSubtree)
	}
	v := subtreeView{path: canonicalViewPath(e.Path), revision: parseRevision(string(e.Revision))}
	switch {
	case e.Error != "":
		v.err = e.Error
	case e.Rules == nil:
		v.err = `the entry holds neither "rules" nor "error"`
	default:
		c := &Chamber{}
		if err := json.Unmarshal(e.Rules, &c.Rules); err != nil {
			v.err = err.Error()
			break
		}
		if c.Rules == nil {
			c.Rules = make(map[string]*Flag)
		}
		v.chamber = c
	}
	return v, nil
}

// canonicalViewPath returns p starting and ending with "/", as the server
// sends paths.
func canonicalViewPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// retrieveSubtree reads the views of root and of every chamber below it.
func (rlm *Realm) retrieveSubtree(ctx context.Context, root string) (_ []subtreeView, err error) {
	ctx, span := rlm.tracer.Start(ctx, "retrieveSubtree", trace.WithAttributes(attribute.String("realm.path", root)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			refreshes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		} else {
			refreshes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
		}
		span.End()
	}()

	res, err := rlm.client.GetSubtree(ctx, strings.TrimPrefix(root, "/"))
	if err != nil {
		return nil, fmt.Errorf("could not get the subtree at %q: %w", root, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not get the subtree at %q: %w", root, newStatusError(res))
	}
	views, err := readSubtreeResponse(res, root)
	if err != nil {
		return nil, fmt.Errorf("could not read the subtree at %q: %w", root, err)
	}
	span.SetAttributes(attribute.Int("realm.chambers", len(views)))
	return views, nil
}

// readSubtreeResponse reads a response envelope holding the subtree at
// root, of at most maxSubtreeBytes. It must hold an entry for root, which
// the server always sends, so that a stray answer never wipes out every
// view.
func readSubtreeResponse(res *http.Response, root string) ([]subtreeView, error) {
	views, err := decodeSubtree(&boundedReader{r: res.Body, n: maxSubtreeBytes})
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(views, func(v subtreeView) bool { return v.path == root }) {
		return nil, fmt.Errorf("%w: it holds no view of its root %s", errNotSubtree, root)
	}
	return views, nil
}

// decodeSubtree decodes a response envelope holding a subtree,
// {"data":{"chambers":[…]}}, one entry at a time. Like readChamberResponse,
// it ignores fields it does not know.
func decodeSubtree(r io.Reader) ([]subtreeView, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, fmt.Errorf("the response is not a realm response: %w", err)
	}
	var (
		views    []subtreeView
		found    bool
		messages []string
	)
	for dec.More() {
		name, err := objectKey(dec)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.EqualFold(name, "data"):
			views, found, err = decodeSubtreeData(dec)
		case strings.EqualFold(name, "errors"):
			err = dec.Decode(&messages)
		default:
			err = dec.Decode(new(json.RawMessage))
		}
		if err != nil {
			return nil, err
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if len(messages) > 0 {
		return nil, errors.New(strings.Join(messages, "; "))
	}
	if !found {
		return nil, fmt.Errorf(`%w: the response holds no "chambers"`, errNotSubtree)
	}
	return views, nil
}

// decodeSubtreeData decodes the envelope's data, reporting whether it held
// "chambers".
func decodeSubtreeData(dec *json.Decoder) (views []subtreeView, found bool, err error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, false, fmt.Errorf("%w: its data is not an object", errNotSubtree)
	}
	for dec.More() {
		name, err := objectKey(dec)
		if err != nil {
			return nil, false, err
		}
		if !strings.EqualFold(name, "chambers") {
			if err := dec.Decode(new(json.RawMessage)); err != nil {
				return nil, false, err
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return nil, false, fmt.Errorf(`%w: its "chambers" is not a list`, errNotSubtree)
		}
		views = views[:0]
		for dec.More() {
			var e subtreeEntry
			if err := dec.Decode(&e); err != nil {
				return nil, false, err
			}
			v, err := e.decode()
			if err != nil {
				return nil, false, err
			}
			views = append(views, v)
		}
		if err := expectDelim(dec, ']'); err != nil {
			return nil, false, err
		}
		found = true
	}
	return views, found, expectDelim(dec, '}')
}

// expectDelim reads the next token, which must be d.
func expectDelim(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != d {
		return fmt.Errorf("expected %v, found %v", d, tok)
	}
	return nil
}

// objectKey reads the name of an object's next field.
func objectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	name, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected a field name, found %v", tok)
	}
	return name, nil
}

// boundedReader reads from r until n bytes have been read, then fails with
// errTooLarge if r holds more.
type boundedReader struct {
	r io.Reader
	n int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		var one [1]byte
		n, err := b.r.Read(one[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	n, err := b.r.Read(p)
	b.n -= int64(n)
	return n, err
}

// newView returns the read view of the chamber c at path, at the server's
// revision rev. It shares c's rules and realm's process-wide attributes,
// which nothing changes afterwards.
func (rlm *Realm) newView(path string, c *Chamber, rev uint64) *ChamberEntry {
	return &ChamberEntry{rules: c.Rules, attributes: rlm.attributes, version: rlm.semver, revision: rev, path: path}
}

// setSubtreeIf replaces the subtree snapshot with views, fetched while the
// snapshot was at version, unless the snapshot has changed since. A view that
// could not be built keeps the one the snapshot holds for its path, if any.
// It reports whether it replaced the snapshot.
func (rlm *Realm) setSubtreeIf(ctx context.Context, views []subtreeView, version uint64) bool {
	m := make(map[string]*ChamberEntry, len(views))
	var unbuilt []subtreeView
	for _, v := range views {
		if v.chamber == nil {
			unbuilt = append(unbuilt, v)
			continue
		}
		m[v.path] = rlm.newView(v.path, v.chamber, v.revision)
	}
	rlm.mu.Lock()
	if rlm.version != version {
		rlm.mu.Unlock()
		return false
	}
	for _, v := range unbuilt {
		if _, ok := m[v.path]; ok {
			continue
		}
		if old, ok := rlm.views[v.path]; ok {
			m[v.path] = old
		}
	}
	rlm.views = m
	rlm.version++
	rlm.refreshed = time.Now()
	rlm.mu.Unlock()

	rlm.noteUnsupported(ctx, views...)
	if len(unbuilt) == 0 {
		if n, reported := rlm.badViews.ok(); reported {
			rlm.logger.InfoCtx(ctx).Int("failures", n).Msg("every chamber view of the subtree could be built again")
		}
	} else if log, n := rlm.badViews.fail(time.Now()); log {
		rlm.logger.WarnCtx(ctx).Str("path", unbuilt[0].path).Str("error", unbuilt[0].err).Int("views", len(unbuilt)).Int("failures", n).
			Msg("the realm server could not build some chamber views of the subtree; keeping the last ones received for them")
	}
	return true
}

// noteUnsupported logs, once each, the flags of views that this SDK cannot
// fully evaluate (see Realm.replace).
func (rlm *Realm) noteUnsupported(ctx context.Context, views ...subtreeView) {
	var found map[string]string
	for _, v := range views {
		if v.chamber == nil {
			continue
		}
		for _, key := range v.chamber.unsupportedFlags() {
			if found == nil {
				found = make(map[string]string)
			}
			if _, ok := found[key]; !ok {
				found[key] = v.path
			}
		}
	}
	if len(found) == 0 {
		return
	}
	rlm.mu.Lock()
	if rlm.unsupportedSeen == nil {
		rlm.unsupportedSeen = make(map[string]struct{}, len(found))
	}
	var fresh []string
	for key := range found {
		if _, ok := rlm.unsupportedSeen[key]; !ok {
			rlm.unsupportedSeen[key] = struct{}{}
			fresh = append(fresh, key)
		}
	}
	rlm.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	slices.Sort(fresh)
	rlm.logger.WarnCtx(ctx).Strs("flags", fresh).Str("path", found[fresh[0]]).Msg("these flags use flag types or targeting operators this version of the realm SDK does not support: a flag of an unknown type returns the caller's default, and a rule with an unknown operator never matches; upgrade the SDK")
}

// streamHandler applies what one stream sends: a single chamber, or a
// subtree.
type streamHandler interface {
	// open opens the stream.
	open(ctx context.Context) (*http.Response, error)
	// answered handles an answer that is not an event stream, received while
	// the snapshot was at version. It returns errNotStreaming when it applied
	// what the server sent instead (the server predates streaming).
	answered(ctx context.Context, res *http.Response, version uint64) error
	// event handles one event, reporting whether the stream has delivered
	// (see Realm.streamDelivered), or an error that ends the stream.
	event(ctx context.Context, name, id string, data []byte) (bool, error)
}

// chamberStream is the stream of the chamber at the Realm's path.
type chamberStream struct{ rlm *Realm }

func (s chamberStream) open(ctx context.Context) (*http.Response, error) {
	return s.rlm.client.Watch(ctx, s.rlm.path)
}

func (s chamberStream) answered(ctx context.Context, res *http.Response, version uint64) error {
	// A server that predates streaming ignores ?watch=true and answers with
	// the chamber. Anything else (a proxy's sign-in page, say) is a failed
	// attempt, retried as a stream.
	c, rev, err := readChamberResponse(res)
	if err != nil {
		return fmt.Errorf("chamber stream: the server answered with neither an event stream nor a chamber: %w", err)
	}
	s.rlm.setChamberIf(c, rev, version)
	return errNotStreaming
}

func (s chamberStream) event(ctx context.Context, name, id string, data []byte) (bool, error) {
	// Only chamber events (or unnamed ones) carry a chamber; others, which a
	// newer server may send, are skipped.
	if name != "" && name != "chamber" {
		return false, nil
	}
	return s.rlm.applyEvent(ctx, data, parseRevision(id)), nil
}

// subtreeStream is the stream of the subtree at the Realm's template root.
// Until the stream's first synced event, it stages the snapshot the server
// is sending, which then replaces the Realm's; after that, it applies each
// change as it arrives.
type subtreeStream struct {
	rlm *Realm
	// synced is set once the first synced event has arrived.
	synced bool
	// staged holds the views received before it.
	staged map[string]*ChamberEntry
	// sent holds the paths the server sent since the last synced event, so
	// the next one can drop the paths it did not.
	sent map[string]struct{}
}

func newSubtreeStream(rlm *Realm) *subtreeStream {
	return &subtreeStream{rlm: rlm, staged: make(map[string]*ChamberEntry), sent: make(map[string]struct{})}
}

func (s *subtreeStream) open(ctx context.Context) (*http.Response, error) {
	return s.rlm.client.WatchSubtree(ctx, s.rlm.template.root)
}

func (s *subtreeStream) answered(ctx context.Context, res *http.Response, version uint64) error {
	views, err := readSubtreeResponse(res, s.rlm.template.root)
	if err != nil {
		return fmt.Errorf("subtree stream: the server answered with neither an event stream nor a subtree: %w", err)
	}
	s.rlm.setSubtreeIf(ctx, views, version)
	return errNotStreaming
}

func (s *subtreeStream) event(ctx context.Context, name, id string, data []byte) (bool, error) {
	switch name {
	case "chamber":
		return false, s.chamber(ctx, id, data)
	case "deleted":
		return false, s.deleted(ctx, data)
	case "synced":
		s.sync(data)
		return true, nil
	}
	// Events with other names, which newer servers may send, are skipped.
	return false, nil
}

// chamber applies a chamber event. One that names no path ends the stream:
// the server does not stream subtrees.
func (s *subtreeStream) chamber(ctx context.Context, id string, data []byte) error {
	var e subtreeEntry
	if err := json.Unmarshal(data, &e); err != nil {
		s.rlm.badEvent(ctx, "", err)
		return nil
	}
	v, err := e.decode()
	if err != nil {
		return fmt.Errorf("subtree stream: %w", err)
	}
	if id != "" {
		v.revision = parseRevision(id)
	}
	s.sent[v.path] = struct{}{}
	if v.chamber == nil {
		s.rlm.badEvent(ctx, v.path, errors.New(v.err))
		return nil
	}
	_, _ = s.rlm.badEvents.ok()
	view := s.rlm.newView(v.path, v.chamber, v.revision)
	s.rlm.noteUnsupported(ctx, v)
	if !s.synced {
		s.staged[v.path] = view
		return nil
	}
	s.rlm.mu.Lock()
	if s.rlm.views == nil {
		s.rlm.views = make(map[string]*ChamberEntry)
	}
	s.rlm.views[v.path] = view
	s.rlm.changed()
	s.rlm.mu.Unlock()
	return nil
}

// deleted applies a deleted event.
func (s *subtreeStream) deleted(ctx context.Context, data []byte) error {
	var e subtreeEntry
	if err := json.Unmarshal(data, &e); err != nil {
		s.rlm.badEvent(ctx, "", err)
		return nil
	}
	if e.Path == "" {
		return fmt.Errorf(`subtree stream: %w: a deleted event has no "path"`, errNotSubtree)
	}
	path := canonicalViewPath(e.Path)
	delete(s.sent, path)
	if !s.synced {
		delete(s.staged, path)
		return nil
	}
	s.rlm.mu.Lock()
	if _, ok := s.rlm.views[path]; ok {
		delete(s.rlm.views, path)
		s.rlm.changed()
	}
	s.rlm.mu.Unlock()
	return nil
}

// sync handles a synced event, which ends a snapshot of the subtree: the
// first replaces the Realm's snapshot with the staged one, and every one
// drops the paths the server did not send since the last.
//
// A view the server could not build is not sent, but counts in the event's
// "chambers". When fewer paths were sent than that, the client cannot tell
// a deleted chamber from one that could not be built, so it keeps the views
// of the paths not sent: dropping a view it cannot rebuild would serve its
// parent's instead.
func (s *subtreeStream) sync(data []byte) {
	var body struct {
		Chambers *int `json:"chambers"`
	}
	complete := json.Unmarshal(data, &body) != nil || body.Chambers == nil || len(s.sent) >= *body.Chambers
	sent := s.sent
	s.sent = make(map[string]struct{}, len(sent))

	rlm := s.rlm
	rlm.mu.Lock()
	defer rlm.mu.Unlock()
	if !s.synced {
		s.synced = true
		staged := s.staged
		s.staged = nil
		for path, view := range rlm.views {
			if _, ok := staged[path]; ok {
				continue
			}
			// sent but not built, or perhaps not built
			if _, ok := sent[path]; ok || !complete {
				staged[path] = view
			}
		}
		rlm.views = staged
		rlm.changed()
		return
	}
	if !complete {
		return
	}
	n := len(rlm.views)
	maps.DeleteFunc(rlm.views, func(path string, _ *ChamberEntry) bool {
		_, ok := sent[path]
		return !ok
	})
	if len(rlm.views) != n {
		rlm.changed()
	}
}

// changed records a change to the subtree snapshot. rlm.mu must be held.
func (rlm *Realm) changed() {
	rlm.version++
	rlm.refreshed = time.Now()
}

// badEvent logs, rate-limited, a streamed view that was skipped.
func (rlm *Realm) badEvent(ctx context.Context, path string, err error) {
	if log, n := rlm.badEvents.fail(time.Now()); log {
		ev := rlm.logger.WarnCtx(ctx).Err(err).Int("failures", n)
		if path != "" {
			ev = ev.Str("path", path)
		}
		ev.Msg("skipped a streamed chamber that could not be read; keeping the last one")
	}
}
