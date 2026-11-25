package realm

import (
	"context"
	"time"
)

// snapshot is what flag reads evaluate against, and how it is fetched and
// kept current: the chamber at one path (WithPath, chamberSnapshot), or the
// views of a subtree that a path template picks from (WithPathTemplate,
// subtreeSnapshot). Realm.mu guards each one's state: loaded, pick, and
// pinned are called with it held, at least for reading.
type snapshot interface {
	// readPath returns the path realm reads: the chamber's, or the
	// template's root.
	readPath() string
	// loaded reports whether a load or a stream has delivered it.
	loaded() bool
	// pick returns the view flag reads with ec use, or nil when there is
	// none yet. ec is as the caller gave it, before WithAttributes.
	pick(ec EvaluationContext) *ChamberEntry
	// pinned returns the view NewContext pins: the chamber, or the view of
	// the template's root; nil when there is none yet.
	pinned() *ChamberEntry
	// fetch fetches the snapshot and applies it, unless the snapshot
	// changed from version while it was being fetched.
	fetch(ctx context.Context, version uint64) error
	// stream returns the handler of one stream of the snapshot.
	stream() streamHandler
}

// chamberSnapshot is the snapshot of the chamber at one path.
type chamberSnapshot struct {
	rlm *Realm
	// path is the chamber's path, as given to WithPath.
	path string
	// entry is the chamber; nil until the first load.
	entry *ChamberEntry
}

func (s *chamberSnapshot) readPath() string                     { return s.path }
func (s *chamberSnapshot) loaded() bool                         { return s.entry != nil }
func (s *chamberSnapshot) pick(EvaluationContext) *ChamberEntry { return s.entry }
func (s *chamberSnapshot) pinned() *ChamberEntry                { return s.entry }
func (s *chamberSnapshot) stream() streamHandler                { return chamberStream{s} }

func (s *chamberSnapshot) fetch(ctx context.Context, version uint64) error {
	c, rev, err := s.rlm.retrieveChamber(ctx, s.path)
	if err != nil {
		return err
	}
	s.setIf(ctx, c, rev, version)
	return nil
}

// set replaces the chamber with c, at the server's revision rev (0 when
// unknown).
func (s *chamberSnapshot) set(ctx context.Context, c *Chamber, rev uint64) {
	s.replace(ctx, c, rev, false, 0)
}

// setIf replaces the chamber with c, at the server's revision rev, which was
// fetched while the snapshot was at version, unless the snapshot has changed
// since: a newer chamber (from the stream, say) arrived while c was in
// flight. It reports whether it replaced the chamber.
func (s *chamberSnapshot) setIf(ctx context.Context, c *Chamber, rev uint64, version uint64) bool {
	return s.replace(ctx, c, rev, true, version)
}

func (s *chamberSnapshot) replace(ctx context.Context, c *Chamber, rev uint64, conditional bool, version uint64) bool {
	rlm := s.rlm
	entry := rlm.newView(s.path, c, rev)
	rlm.mu.Lock()
	if conditional && rlm.version != version {
		rlm.mu.Unlock()
		return false
	}
	s.entry = entry
	rlm.changed()
	rlm.mu.Unlock()
	rlm.noteUnsupported(ctx, subtreeView{path: s.path, chamber: c})
	return true
}

// subtreeSnapshot is the snapshot of the subtree at a path template's root:
// the view of each path at or below it, which the template picks from.
type subtreeSnapshot struct {
	rlm      *Realm
	template *pathTemplate
	// views holds the view of each path; nil until the first load. The
	// views are immutable; the map changes in place, so a change costs the
	// same however large the subtree.
	views map[string]*ChamberEntry
}

func (s *subtreeSnapshot) readPath() string      { return s.template.root }
func (s *subtreeSnapshot) loaded() bool          { return s.views != nil }
func (s *subtreeSnapshot) pinned() *ChamberEntry { return s.views[s.template.root] }
func (s *subtreeSnapshot) stream() streamHandler { return newSubtreeStream(s) }

func (s *subtreeSnapshot) pick(ec EvaluationContext) *ChamberEntry {
	return s.template.resolve(s.views, ec.withDefaults(s.rlm.attributes))
}

func (s *subtreeSnapshot) fetch(ctx context.Context, version uint64) error {
	views, err := s.rlm.retrieveSubtree(ctx, s.template.root)
	if err != nil {
		return err
	}
	s.setIf(ctx, views, version)
	return nil
}

// setIf replaces the views with views, fetched while the snapshot was at
// version, unless the snapshot has changed since. A view that could not be
// built keeps the one the snapshot holds for its path, if any. It reports
// whether it replaced the views.
func (s *subtreeSnapshot) setIf(ctx context.Context, views []subtreeView, version uint64) bool {
	rlm := s.rlm
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
		if old, ok := s.views[v.path]; ok {
			m[v.path] = old
		}
	}
	s.views = m
	rlm.changed()
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

// changed records a change to the snapshot. rlm.mu must be held.
func (rlm *Realm) changed() {
	rlm.version++
	rlm.refreshed = time.Now()
}
