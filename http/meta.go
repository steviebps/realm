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

// maxHistory is the number of revisions retained per chamber.
const maxHistory = 200

// ActorHeader identifies who made a change, recorded in chamber history.
const ActorHeader = "X-Realm-Actor"

// reservedName is the root-level storage name of realm's metadata.
var reservedName = strings.Trim(storage.ReservedPrefix, "/")

// isReservedName reports whether a root listing entry is realm's metadata.
// Backends format directory names differently ("_realm", "_realm/", or a
// nested "_realm/catalog"), so match the segment exactly.
func isReservedName(name string) bool {
	segment, _, _ := strings.Cut(name, "/")
	return segment == reservedName
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

// historyIndex lists revision summaries, newest first.
type historyIndex struct {
	Revisions []Revision `json:"revisions"`
}

// metaStore keeps realm metadata in raw (non-inheriting) storage.
type metaStore struct {
	strg      storage.Storage
	catalogMu sync.Mutex
}

// historyIndexKey encodes the chamber path as a single storage segment so
// nested chambers never collide with one another's history.
func historyIndexKey(chamberPath string) string {
	return storage.ReservedPrefix + "history/" + url.PathEscape(chamberPath) + "/"
}

func revisionKey(chamberPath string, rev int) string {
	return historyIndexKey(chamberPath) + strconv.Itoa(rev) + "/"
}

// get returns the entry stored at key, or nil when it does not exist.
func (m *metaStore) get(ctx context.Context, key string) (*storage.StorageEntry, error) {
	entry, err := m.strg.Get(ctx, key)
	var nf *storage.NotFoundError
	if errors.As(err, &nf) {
		return nil, nil
	}
	return entry, err
}

// appendHistory records a change. Callers must hold the path lock.
func (m *metaStore) appendHistory(ctx context.Context, chamberPath string, rev Revision) error {
	var idx historyIndex
	if _, err := storage.ReadJSON(ctx, m.strg, historyIndexKey(chamberPath), &idx); err != nil {
		return fmt.Errorf("could not read history index: %w", err)
	}

	rev.Revision = 1
	if len(idx.Revisions) > 0 {
		rev.Revision = idx.Revisions[0].Revision + 1
	}
	if err := storage.WriteJSON(ctx, m.strg, revisionKey(chamberPath, rev.Revision), rev); err != nil {
		return fmt.Errorf("could not write revision: %w", err)
	}

	summary := rev
	summary.Chamber = nil
	idx.Revisions = append([]Revision{summary}, idx.Revisions...)
	var expired []Revision
	if len(idx.Revisions) > maxHistory {
		expired = idx.Revisions[maxHistory:]
		idx.Revisions = idx.Revisions[:maxHistory]
	}
	if err := storage.WriteJSON(ctx, m.strg, historyIndexKey(chamberPath), idx); err != nil {
		return fmt.Errorf("could not write history index: %w", err)
	}
	for _, old := range expired {
		_ = m.strg.Delete(ctx, revisionKey(chamberPath, old.Revision))
	}
	return nil
}

// history returns revision summaries, newest first.
func (m *metaStore) history(ctx context.Context, chamberPath string) ([]Revision, error) {
	var idx historyIndex
	if _, err := storage.ReadJSON(ctx, m.strg, historyIndexKey(chamberPath), &idx); err != nil {
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
	found, err := storage.ReadJSON(ctx, m.strg, revisionKey(chamberPath, rev), &r)
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

const catalogKey = storage.ReservedPrefix + "catalog/"

func (m *metaStore) catalog(ctx context.Context) (Catalog, error) {
	c := Catalog{}
	if _, err := storage.ReadJSON(ctx, m.strg, catalogKey, &c); err != nil {
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
	return c, storage.WriteJSON(ctx, m.strg, catalogKey, c)
}
