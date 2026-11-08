package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newBolt(t *testing.T, dir string) *BoltStorage {
	t.Helper()
	s, err := NewBoltStorage(map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("NewBoltStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s.(*BoltStorage)
}

// writeRawBolt writes entries straight into the bbolt file in dir, bucket by
// bucket, as an earlier version of realm (or a damaged database) would have
// left them.
func writeRawBolt(t *testing.T, dir string, buckets map[string]map[string]string) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dir, "realm.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		for name, entries := range buckets {
			b, err := tx.CreateBucketIfNotExists([]byte(name))
			if err != nil {
				return err
			}
			for k, v := range entries {
				if err := b.Put([]byte(k), []byte(v)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBoltUpgradesDatabaseWithoutVersions opens a database written before
// entries had versions: every entry gets one, so a version check can tell an
// existing entry from a missing one, and opening it again changes nothing.
func TestBoltUpgradesDatabaseWithoutVersions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeRawBolt(t, dir, map[string]map[string]string{
		"chambers": {"/a": `{"rules":{}}`, "/a/b": `{"rules":{}}`},
	})

	s := newBolt(t, dir)
	va, vb := versionOf(t, s, "/a/"), versionOf(t, s, "/a/b/")
	if va == 0 || va != vb {
		t.Fatalf("versions after upgrade = %d, %d; want one nonzero revision", va, vb)
	}
	if err := s.Txn(ctx, []Check{{Key: "/a/"}}, []Op{put("/a/", `{}`)}); !errors.Is(err, ErrConflict) {
		t.Errorf("creating an upgraded entry = %v, want ErrConflict", err)
	}
	if err := s.Txn(ctx, []Check{{Key: "/a/", Version: va}}, []Op{put("/a/", `{"rules":{"x":{}}}`)}); err != nil {
		t.Errorf("updating at the upgraded version: %v", err)
	}
	va2 := versionOf(t, s, "/a/")
	if va2 <= va {
		t.Errorf("version after an update = %d, want more than %d", va2, va)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	s = newBolt(t, dir)
	if got, got2 := versionOf(t, s, "/a/"), versionOf(t, s, "/a/b/"); got != va2 || got2 != vb {
		t.Errorf("versions after reopening = %d, %d; want %d, %d", got, got2, va2, vb)
	}
	mustPut(t, s, "/c/", `{}`)
	if v := versionOf(t, s, "/c/"); v <= va2 {
		t.Errorf("version of a write after reopening = %d, want more than %d", v, va2)
	}
}

// TestBoltIgnoresVersionsOfMissingEntries checks that a version left behind
// for an entry that no longer exists (by a version of realm that did not
// track versions) does not make the entry look present.
func TestBoltIgnoresVersionsOfMissingEntries(t *testing.T) {
	dir := t.TempDir()
	writeRawBolt(t, dir, map[string]map[string]string{
		"chambers": {"/kept": `{}`},
		"versions": {"/kept": "\x00\x00\x00\x00\x00\x00\x00\x05", "/gone": "\x00\x00\x00\x00\x00\x00\x00\x07"},
		"meta":     {"revision": "\x00\x00\x00\x00\x00\x00\x00\x07"},
	})
	s := newBolt(t, dir)
	if err := s.Txn(context.Background(), []Check{{Key: "/gone/"}}, []Op{put("/gone/", `{}`)}); err != nil {
		t.Fatalf("creating /gone/: %v", err)
	}
	if v := versionOf(t, s, "/gone/"); v != 8 {
		t.Errorf("version of /gone/ = %d, want 8, after the stored revision 7", v)
	}
	if v := versionOf(t, s, "/kept/"); v != 5 {
		t.Errorf("version of /kept/ = %d, want its stored 5", v)
	}
}

// TestBoltUpdateAppliesAllOrNothing checks that an Update whose last op the
// database refuses (a key longer than bbolt allows) applies none of its ops,
// rather than leaving a chamber written without its history.
func TestBoltUpdateAppliesAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := newBolt(t, t.TempDir())
	long := "/" + strings.Repeat("a", bolt.MaxKeySize) + "/"
	err := Update(ctx, s, []string{"/chamber/"}, func(map[string]*StorageEntry) ([]Op, error) {
		return []Op{
			{Key: "/chamber/", Value: json.RawMessage(`{"rules":{}}`)},
			{Key: long, Value: json.RawMessage(`{}`)},
		}, nil
	})
	if err == nil {
		t.Fatal("Update with a key bbolt refuses succeeded")
	}
	if _, err := s.Get(ctx, "/chamber/"); !isNotFound(err) {
		t.Errorf("Get(/chamber/) after the failed update = %v, want NotFoundError: it was half applied", err)
	}
}
