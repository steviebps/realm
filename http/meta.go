package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steviebps/realm/pkg/storage"
)

// reservedPrefix is the storage prefix under which realm keeps its own
// metadata (history, catalog). Chamber APIs reject paths beneath it.
const reservedPrefix = "/_realm/"

// maxHistory is the number of revisions retained per chamber.
const maxHistory = 200

// ActorHeader identifies who made a change, recorded in chamber history.
const ActorHeader = "X-Realm-Actor"

const defaultActor = "anonymous"

// isReservedPath reports whether p falls under realm's metadata prefix.
func isReservedPath(p string) bool {
	return strings.HasPrefix(p, reservedPrefix) || p == strings.TrimSuffix(reservedPrefix, "/")
}

// etagOf returns a strong ETag for stored chamber bytes.
func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

// etagMatches reports whether an If-Match / If-None-Match header value matches
// the current ETag. current is empty when the resource does not exist.
func etagMatches(header, current string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" && current != "" {
			return true
		}
		if candidate != "" && candidate == current {
			return true
		}
	}
	return false
}

// pathLocks serializes read-check-write sequences per chamber path so that
// If-Match checks and history appends are atomic within one server process.
// Locks are striped to bound memory regardless of how many paths exist.
type pathLocks struct {
	stripes [64]sync.Mutex
}

func (l *pathLocks) lock(p string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(p))
	m := &l.stripes[h.Sum32()%uint32(len(l.stripes))]
	m.Lock()
	return m.Unlock
}

// Revision describes one change to a chamber.
type Revision struct {
	Revision  int       `json:"revision"`
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor"`
	Operation Operation `json:"operation"`
	// ETag is the chamber's ETag after the change (empty for deletes).
	ETag string `json:"etag,omitempty"`
	// Chamber is the stored chamber after the change. It is omitted from
	// history listings and null for deletes.
	Chamber json.RawMessage `json:"chamber,omitempty"`
}

type historyIndex struct {
	Latest    int        `json:"latest"`
	Revisions []Revision `json:"revisions"`
}

// metaStore keeps realm metadata in raw (non-inheriting) storage.
type metaStore struct {
	strg      storage.Storage
	catalogMu sync.Mutex
}

// segment encodes a chamber path or attribute name as a single storage path
// segment so nested chambers never collide with one another's metadata.
func segment(s string) string {
	return url.PathEscape(s)
}

func historyIndexKey(chamberPath string) string {
	return reservedPrefix + "history/" + segment(chamberPath) + "/"
}

func revisionKey(chamberPath string, rev int) string {
	return historyIndexKey(chamberPath) + strconv.Itoa(rev) + "/"
}

// readJSON loads the value at key into v. found is false when it does not exist.
func (m *metaStore) readJSON(ctx context.Context, key string, v any) (found bool, err error) {
	entry, err := m.strg.Get(ctx, key)
	if err != nil {
		var nf *storage.NotFoundError
		if errors.As(err, &nf) {
			return false, nil
		}
		return false, err
	}
	return true, json.Unmarshal(entry.Value, v)
}

func (m *metaStore) writeJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return m.strg.Put(ctx, storage.StorageEntry{Key: key, Value: b})
}

// currentETag returns the ETag of the chamber as stored at chamberPath
// (without inheritance), or "" when it does not exist.
func (m *metaStore) currentETag(ctx context.Context, chamberPath string) (string, error) {
	entry, err := m.strg.Get(ctx, chamberPath)
	if err != nil {
		var nf *storage.NotFoundError
		if errors.As(err, &nf) {
			return "", nil
		}
		return "", err
	}
	return etagOf(entry.Value), nil
}

// appendHistory records a change. Callers must hold the path lock.
func (m *metaStore) appendHistory(ctx context.Context, chamberPath string, rev Revision) (Revision, error) {
	var idx historyIndex
	if _, err := m.readJSON(ctx, historyIndexKey(chamberPath), &idx); err != nil {
		return rev, fmt.Errorf("could not read history index: %w", err)
	}

	rev.Revision = idx.Latest + 1
	if err := m.writeJSON(ctx, revisionKey(chamberPath, rev.Revision), rev); err != nil {
		return rev, fmt.Errorf("could not write revision: %w", err)
	}

	summary := rev
	summary.Chamber = nil
	idx.Latest = rev.Revision
	idx.Revisions = append([]Revision{summary}, idx.Revisions...)
	var expired []Revision
	if len(idx.Revisions) > maxHistory {
		expired = idx.Revisions[maxHistory:]
		idx.Revisions = idx.Revisions[:maxHistory]
	}
	if err := m.writeJSON(ctx, historyIndexKey(chamberPath), idx); err != nil {
		return rev, fmt.Errorf("could not write history index: %w", err)
	}
	for _, old := range expired {
		_ = m.strg.Delete(ctx, revisionKey(chamberPath, old.Revision))
	}
	return rev, nil
}

// history returns revision summaries, newest first.
func (m *metaStore) history(ctx context.Context, chamberPath string) ([]Revision, error) {
	var idx historyIndex
	if _, err := m.readJSON(ctx, historyIndexKey(chamberPath), &idx); err != nil {
		return nil, err
	}
	if idx.Revisions == nil {
		idx.Revisions = []Revision{}
	}
	return idx.Revisions, nil
}

// revision returns one revision including its chamber.
func (m *metaStore) revision(ctx context.Context, chamberPath string, rev int) (*Revision, error) {
	var r Revision
	found, err := m.readJSON(ctx, revisionKey(chamberPath, rev), &r)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &storage.NotFoundError{Key: fmt.Sprintf("revision %d of %s", rev, chamberPath)}
	}
	return &r, nil
}

// Catalog maps attribute names to their known values and human-readable
// labels, for example {"locationId": {"7f3c…": "Downtown"}}.
type Catalog map[string]map[string]string

const catalogKey = reservedPrefix + "catalog/"

func (m *metaStore) catalog(ctx context.Context) (Catalog, error) {
	c := Catalog{}
	if _, err := m.readJSON(ctx, catalogKey, &c); err != nil {
		return nil, err
	}
	return c, nil
}

// updateCatalog applies fn to the catalog under a lock and persists the result.
func (m *metaStore) updateCatalog(ctx context.Context, fn func(Catalog) error) (Catalog, error) {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()

	c, err := m.catalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	return c, m.writeJSON(ctx, catalogKey, c)
}
