package storage

import (
	"context"
	"iter"
	"strings"

	"github.com/steviebps/realm/utils"
)

// Scanner is implemented by backends that can read every entry at or below a
// path in one range read, rather than one List per level of the tree and one
// Get per entry. Subtree reads (see ListTree and ReadTree) use it when the
// innermost storage (see Raw) implements it, so it belongs to that storage:
// a wrapper whose reads can differ from its source's, such as cacheable,
// must not pass it on.
type Scanner interface {
	Storage
	// Scan yields the entry at prefix and every entry below it, in byte order
	// of their keys, and ends after a failed read, which it yields as an
	// error. prefix is a rooted path; a trailing slash is implied, so "/prod"
	// and "/prod/" both yield "/prod/" and "/prod/a/" but never
	// "/production/". Each entry's Key is the path it is stored at, rooted
	// and (as every key realm writes is) with a trailing slash, and its Value
	// and Version are what Get returns for that path. At "/", entries under realm's reserved paths
	// (see IsReservedPath) are left out.
	//
	// Backends stream the entries where they can, so a scan may hold a
	// connection (or a page of entries) until the caller stops iterating. A
	// caller must not wait for other reads of the same storage while it
	// iterates: they may need the connection the scan holds.
	Scan(ctx context.Context, prefix string) iter.Seq2[*StorageEntry, error]
	// ScanKeys yields the keys Scan would yield, without reading the
	// entries' values.
	ScanKeys(ctx context.Context, prefix string) iter.Seq2[string, error]
}

// scanPrefix returns the canonical form of a scanned prefix: rooted, with a
// trailing slash.
func scanPrefix(prefix string) string {
	prefix = utils.EnsureTrailingSlash(prefix)
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return prefix
}

// scanRanges returns the key ranges, each [lo, hi), that a scan of the
// canonical prefix reads, in order: every key that begins with prefix, less
// the range under ReservedPrefix when prefix is "/". A key begins with a
// prefix ending in "/" exactly when it sorts at or after the prefix and
// before the prefix with its final "/" replaced by "0", the next byte.
func scanRanges(prefix string) [][2]string {
	end := strings.TrimSuffix(prefix, "/") + "0"
	if prefix != "/" {
		return [][2]string{{prefix, end}}
	}
	return [][2]string{{prefix, ReservedPrefix}, {strings.TrimSuffix(ReservedPrefix, "/") + "0", end}}
}

// scanSkips reports whether a scan of prefix leaves out the entry at key:
// at "/", realm's reserved paths are not part of any subtree. Backends skip
// the range under ReservedPrefix itself without reading it; this catches the
// spellings IsReservedPath also treats as reserved ("/_REALM/", say).
func scanSkips(prefix, key string) bool {
	return prefix == "/" && IsReservedPath(key)
}

// scanKeysOf yields the key of each entry entries yields.
func scanKeysOf(entries iter.Seq2[*StorageEntry, error]) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for e, err := range entries {
			if err != nil {
				yield("", err)
				return
			}
			if !yield(e.Key, nil) {
				return
			}
		}
	}
}
