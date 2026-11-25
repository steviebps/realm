package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// DefaultMaxSubtreeChambers is the default cap on the chambers a subtree read
// or watch may hold (see HandlerConfig.MaxSubtreeChambers).
const DefaultMaxSubtreeChambers = 10000

// subtreeFlushEvery is how many entries a subtree read writes between
// flushes, so clients receive a large subtree as it is read.
const subtreeFlushEvery = 64

// subtreeEntry is one view in a subtree read: its rules, or why they cannot
// be built.
type subtreeEntry struct {
	Path string `json:"path"`
	// Rules is never omitted when the view was built, even when empty.
	Rules    map[string]*realm.Flag `json:"rules,omitzero"`
	Revision uint64                 `json:"revision,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// subtreeEvent is the data of a subtree stream's chamber and deleted events:
// the view of a path, why it cannot be built, or (for deleted) the path
// alone.
type subtreeEvent struct {
	Path  string                 `json:"path"`
	Rules map[string]*realm.Flag `json:"rules,omitzero"`
	Error string                 `json:"error,omitempty"`
}

// syncedEvent is the data of a subtree stream's synced event.
type syncedEvent struct {
	// Chambers counts the views the snapshot held, including any it could
	// not build.
	Chambers int `json:"chambers"`
}

// subtrees serves subtree reads and watches (GET
// /v1/chambers/<root>?subtree=true, with or without watch=true): the view of
// a root and of every chamber below it.
type subtrees struct {
	strg  storage.Storage
	views *sharedViews
	brk   *broker
	// limit caps the chambers below a root (see
	// HandlerConfig.MaxSubtreeChambers).
	limit int
	// compress gzips responses to requests that accept it (see
	// HandlerConfig.DisableSubtreeCompression).
	compress bool
}

// list lists the subtree at root, responding to errors with fail.
func (s *subtrees) list(ctx context.Context, root string, fail func(int, error)) ([]string, bool) {
	paths, err := storage.ListTree(ctx, s.strg, root, s.limit)
	switch {
	case errors.Is(err, storage.ErrSubtreeTooLarge):
		fail(http.StatusRequestEntityTooLarge, fmt.Errorf("%w; read a smaller subtree", err))
	case errors.Is(err, storage.ErrInvalidPath):
		fail(http.StatusBadRequest, err)
	case err != nil:
		fail(http.StatusInternalServerError, err)
	default:
		return paths, true
	}
	return nil, false
}

// serve responds with the view of root and every chamber below it, written
// as they are read (see docs/design/subtree.md). The caller has checked that
// the request may read root, which covers the paths below it. A failure
// partway through ends the body early, so clients see JSON that does not
// parse and retry. The body is gzipped when the request accepts it, and
// each flush sends the entries written so far.
func (s *subtrees) serve(w http.ResponseWriter, r *http.Request, root string, fail func(int, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.subtree", true))
	varyByEncoding(w)

	paths, ok := s.list(ctx, root, fail)
	if !ok {
		return
	}
	principal := auth.FromContext(ctx)

	w, end := compressible(w, r, s.compress)
	defer end()
	span.SetAttributes(attribute.String("realm.server.contentEncoding", contentEncoding(w)))
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, `{"data":{"chambers":[`); err != nil {
		return
	}
	n := 0
	for v, err := range storage.ReadTree(ctx, s.strg, paths) {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			noteFailure(ctx, http.StatusInternalServerError, fmt.Errorf("the subtree read failed after %d chambers: %w", n, err))
			return
		}
		entry := subtreeEntry{Path: v.Path, Revision: v.Revision}
		if v.Err != nil {
			entry.Error = visibleError(principal, v.Err)
			logger.WarnCtx(ctx).Str("path", v.Path).Str("error", v.Err.Error()).Msg("the inherited view cannot be built")
		} else {
			entry.Rules = v.Chamber.Rules
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			noteFailure(ctx, http.StatusInternalServerError, err)
			return
		}
		if n > 0 {
			raw = append([]byte{','}, raw...)
		}
		if _, err := w.Write(raw); err != nil {
			return // the client is gone
		}
		n++
		if n%subtreeFlushEvery == 0 {
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
	span.SetAttributes(attribute.Int("realm.server.subtree.chambers", n))
	_, _ = io.WriteString(w, "]}}\n")
}

// stream holds an SSE connection open and keeps the client's views of root
// and every chamber below it current (see docs/design/subtree.md): a
// snapshot of every view, then synced, then the views each change affects.
// The stream is gzipped when the request accepts it, and each flush (a
// batch of events, or a heartbeat) sends what was written so far. The
// caller has checked that the request may read root.
func (s *subtrees) stream(w http.ResponseWriter, r *http.Request, root string, fail func(int, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.watch", true), attribute.Bool("realm.server.subtree", true))
	varyByEncoding(w)

	var sub *treeSubscription
	var paths []string
	stream := openStream(ctx, w, func() (<-chan struct{}, func(), bool) {
		var unsubscribe func()
		var ok bool
		if sub, unsubscribe, ok = s.brk.SubscribeTree(root); !ok {
			return nil, nil, false
		}
		return sub.C(), unsubscribe, true
	}, func(w http.ResponseWriter) (http.ResponseWriter, func(), bool) {
		// listed after subscribing, so a chamber created meanwhile is
		// listed, announced, or both; and before responding, so an
		// oversized subtree is refused with a status
		var ok bool
		if paths, ok = s.list(ctx, root, fail); !ok {
			return nil, nil, false
		}
		cw, end := compressible(w, r, s.compress)
		return cw, end, true
	})
	if stream == nil {
		return
	}
	defer stream.close()

	st := &subtreeStream{subtrees: s, principal: auth.FromContext(ctx), conn: stream.conn, sub: sub, root: root, pending: map[string]struct{}{}}
	retry := stream.retry

	// attempt runs step, and schedules it to run again after a failed read.
	// It returns false when the stream should end.
	attempt := func(step func() error) bool {
		err := step()
		switch {
		case err == nil:
			retry.succeed()
			return true
		case ctx.Err() != nil, errors.Is(err, errStreamGone):
			return false
		case errors.Is(err, storage.ErrSubtreeTooLarge), errors.Is(err, storage.ErrInvalidPath):
			// the client reconnects, and is told why
			logger.WarnCtx(ctx).Str("path", root).Str("error", err.Error()).Msg("ending a subtree stream")
			return false
		}
		delay := retry.fail()
		// shared reads log their failures themselves, once for every stream
		logger.DebugCtx(ctx).Str("path", root).Str("error", err.Error()).Dur("retryIn", delay).Msg("could not read the subtree for a stream")
		return true
	}
	update := func() bool { return attempt(func() error { return st.update(ctx) }) }

	st.resyncDue = true
	if !attempt(func() error { return st.snapshot(ctx, paths) }) {
		return
	}
	onSignal := func() bool {
		st.take()
		return update()
	}
	stream.run(ctx, onSignal, update, nil)
}

// errStreamGone reports that a stream's client is gone.
var errStreamGone = errors.New("the stream's client is gone")

// heldView is what a subtree stream last sent for a path it holds.
type heldView struct {
	// sum is the hash of the data last sent.
	sum [sha256.Size]byte
}

// subtreeStream is the state of one subtree watch.
type subtreeStream struct {
	*subtrees
	// principal is who the stream is for: it is told why a view cannot be
	// built only when it may read the chamber at fault (see mayKnowWhy).
	principal *auth.Principal
	conn      *streamConn
	sub       *treeSubscription
	root      string
	// held maps each path the client holds a view of (the root, and every
	// path below it that holds a chamber) to what was last sent for it.
	held map[string]heldView
	// pending holds the paths below the root whose changes are not sent yet.
	pending map[string]struct{}
	// pendingAll reports a change at or above the root not sent yet: it may
	// change every view.
	pendingAll bool
	// resyncDue reports that the client needs a whole snapshot: the
	// stream cannot tell which views changed, or the last snapshot failed.
	resyncDue bool
}

// take moves the changes the broker recorded into pending.
func (s *subtreeStream) take() {
	changed, overflow := s.sub.Take()
	if overflow {
		s.resyncDue = true
	}
	for _, c := range changed {
		switch {
		case storage.IsReservedPath(c):
			// realm's metadata is never a chamber
		case strings.HasPrefix(s.root, c):
			s.pendingAll = true
		case strings.HasPrefix(c, s.root):
			s.pending[c] = struct{}{}
		}
	}
}

// snapshot sends the view of every path in paths (from ListTree), views
// that cannot be built included; then deleted for every path sent before
// that the snapshot no longer holds, as clients cannot tell where a resync
// starts; then synced. It holds the views sent as the client's.
func (s *subtreeStream) snapshot(ctx context.Context, paths []string) error {
	if err := s.views.prefill(ctx, paths); err != nil {
		return err
	}
	held := make(map[string]heldView, len(paths))
	// a snapshot cut short has sent some views: remember them, so the next
	// one says which of them went
	defer func() {
		if s.resyncDue {
			if s.held == nil {
				s.held = map[string]heldView{}
			}
			maps.Copy(s.held, held)
		}
	}()
	for _, p := range paths {
		v, err := s.views.view(ctx, p)
		if err != nil {
			return err
		}
		if p != s.root && !v.Stored {
			continue // a path that only leads to chambers below it
		}
		data, sum := v.event(s.principal)
		if !s.conn.buffer(eventText("chamber", v.id, data)) {
			return errStreamGone
		}
		held[p] = heldView{sum: sum}
	}
	var gone []string
	for p := range s.held {
		if _, ok := held[p]; !ok {
			gone = append(gone, p)
		}
	}
	slices.Sort(gone)
	for _, p := range gone {
		data, _ := json.Marshal(subtreeEvent{Path: p})
		if !s.conn.buffer(eventText("deleted", 0, data)) {
			return errStreamGone
		}
	}
	data, _ := json.Marshal(syncedEvent{Chambers: len(held)})
	if !s.conn.buffer(eventText("synced", 0, data)) || !s.conn.flush() {
		return errStreamGone
	}
	s.held = held
	s.resyncDue = false
	return nil
}

// resync lists the subtree again and sends a whole snapshot.
func (s *subtreeStream) resync(ctx context.Context) error {
	// The snapshot reads every view after this, so it covers every change
	// recorded so far.
	s.sub.Take()
	clear(s.pending)
	s.pendingAll = false
	paths, err := storage.ListTree(ctx, s.strg, s.root, s.limit)
	if err != nil {
		return err
	}
	return s.snapshot(ctx, paths)
}

// update sends the views that pending changes affect and that differ from
// what the client holds, and deleted for the paths that no longer hold a
// chamber, flushing them together. After a failed read the changes stay
// pending.
func (s *subtreeStream) update(ctx context.Context) error {
	if s.resyncDue {
		return s.resync(ctx)
	}
	if !s.pendingAll && len(s.pending) == 0 {
		return nil
	}
	sent := false
	flush := func() error {
		if sent && !s.conn.flush() {
			return errStreamGone
		}
		return nil
	}
	for _, p := range s.affected() {
		v, err := s.views.view(ctx, p)
		if err != nil {
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return err
		}
		h, held := s.held[p]
		data, sum := v.event(s.principal)
		switch {
		case p != s.root && !v.Stored:
			if !held {
				continue
			}
			delete(s.held, p)
			data, _ := json.Marshal(subtreeEvent{Path: p})
			if !s.conn.buffer(eventText("deleted", 0, data)) {
				return errStreamGone
			}
			sent = true
		case held && h.sum == sum:
			// unchanged, or failing as it did
		default:
			// a view that cannot be built is sent with its error: the
			// client keeps the last view it received
			if !s.conn.buffer(eventText("chamber", v.id, data)) {
				return errStreamGone
			}
			s.held[p] = heldView{sum: sum}
			sent = true
		}
	}
	clear(s.pending)
	s.pendingAll = false
	return flush()
}

// affected returns the paths whose views the pending changes may have
// changed, parents first: each changed path below the root, which may have
// been created or deleted, and each held path at or below one; with a change
// at or above the root, the root and every held path.
func (s *subtreeStream) affected() []string {
	set := make(map[string]struct{}, len(s.pending))
	for p := range s.pending {
		set[p] = struct{}{}
	}
	if s.pendingAll {
		set[s.root] = struct{}{}
	}
	for h := range s.held {
		if s.pendingAll {
			set[h] = struct{}{}
			continue
		}
		// a change at h, or at any path between the root and h
		for i := len(s.root); i < len(h); i++ {
			if h[i] != '/' {
				continue
			}
			if _, ok := s.pending[h[:i+1]]; ok {
				set[h] = struct{}{}
				break
			}
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for p := range set {
			if !yield(p) {
				return
			}
		}
	})
}

// subtreeQuery reports whether r asks for a subtree (?subtree=true),
// refusing combinations a subtree cannot serve with fail.
func subtreeQuery(r *http.Request, fail func(int, error)) (subtree, ok bool) {
	subtree, err := queryBool(r, "subtree")
	if err != nil {
		fail(http.StatusBadRequest, err)
		return false, false
	}
	if !subtree {
		return false, true
	}
	if inherit := r.URL.Query().Get("inherit"); inherit == "false" || inherit == "layers" {
		fail(http.StatusBadRequest, fmt.Errorf("subtree=true cannot be combined with inherit=%s", inherit))
		return false, false
	}
	return true, true
}
