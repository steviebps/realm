package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
	strg storage.Storage
}

// historyIndexKey encodes the chamber path as a single storage segment so
// nested chambers never collide with one another's history.
func historyIndexKey(chamberPath string) string {
	return storage.ReservedPrefix + "history/" + url.PathEscape(chamberPath) + "/"
}

func revisionKey(chamberPath string, rev int) string {
	return historyIndexKey(chamberPath) + strconv.Itoa(rev) + "/"
}

// historyOps returns the storage changes that record rev in the history of
// chamberPath, given its current history index (nil when there is none).
func historyOps(index *storage.StorageEntry, chamberPath string, rev Revision) ([]storage.Op, error) {
	var idx historyIndex
	if index != nil {
		if err := json.Unmarshal(index.Value, &idx); err != nil {
			return nil, fmt.Errorf("could not read history index: %w", err)
		}
	}

	rev.Revision = 1
	if len(idx.Revisions) > 0 {
		rev.Revision = idx.Revisions[0].Revision + 1
	}
	revBytes, err := json.Marshal(rev)
	if err != nil {
		return nil, err
	}

	summary := rev
	summary.Chamber = nil
	idx.Revisions = append([]Revision{summary}, idx.Revisions...)
	var expired []Revision
	if len(idx.Revisions) > maxHistory {
		expired = idx.Revisions[maxHistory:]
		idx.Revisions = idx.Revisions[:maxHistory]
	}
	idxBytes, err := json.Marshal(idx)
	if err != nil {
		return nil, err
	}

	ops := []storage.Op{
		{Key: revisionKey(chamberPath, rev.Revision), Value: revBytes},
		{Key: historyIndexKey(chamberPath), Value: idxBytes},
	}
	for _, old := range expired {
		ops = append(ops, storage.Op{Key: revisionKey(chamberPath, old.Revision)})
	}
	return ops, nil
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

// updateCatalog atomically applies fn to the catalog and persists the result.
func (m *metaStore) updateCatalog(ctx context.Context, fn func(Catalog) error) (Catalog, error) {
	return storage.UpdateJSON(ctx, m.strg, catalogKey, func(c *Catalog) error {
		if *c == nil {
			*c = Catalog{}
		}
		return fn(*c)
	})
}
