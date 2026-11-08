package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steviebps/realm/helper/logging"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// BoltStorage stores entries in a bbolt database file, which only one process
// can open at a time. It is Transactional: every write commits in one bbolt
// transaction at the next value of a revision counter, so an Update's ops
// (a chamber and its history, say) are applied together or not at all.
//
// Values live in the chambers bucket, keyed by the cleaned path, exactly as
// in databases written before versions existed; each entry's revision lives
// under the same key in the versions bucket, and the counter in the meta
// bucket. Opening a database gives entries without a revision one (see
// upgradeVersions).
type BoltStorage struct {
	db     *bolt.DB
	tracer trace.Tracer
}

var (
	_ Storage       = (*BoltStorage)(nil)
	_ Transactional = (*BoltStorage)(nil)
)

var (
	chambersBucket = []byte("chambers")
	versionsBucket = []byte("versions")
	metaBucket     = []byte("meta")
	// revisionKey holds the last revision issued, in metaBucket.
	revisionKey = []byte("revision")
)

// boltLockTimeout bounds how long opening the database waits for another
// process (usually a running server) to release its exclusive lock.
const boltLockTimeout = time.Second

// NewBoltStorage creates a new BoltDB storage backend
func NewBoltStorage(conf map[string]string) (Storage, error) {
	var err error
	path := conf["path"]
	if path == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	file := filepath.Join(path, "realm.db")
	db, err := bolt.Open(file, 0600, &bolt.Options{Timeout: boltLockTimeout})
	if err != nil {
		if errors.Is(err, berrors.ErrTimeout) {
			return nil, fmt.Errorf("%s is locked by another process, such as a running realm server; only one process can open it: %w", file, err)
		}
		return nil, err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{chambersBucket, versionsBucket, metaBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return upgradeVersions(boltBucketsOf(tx))
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &BoltStorage{
		db:     db,
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (b *BoltStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Get", trace.WithAttributes(attribute.String("realm.bolt.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := path.Clean(logicalPath)

	var file []byte
	var version uint64
	err := b.db.View(func(tx *bolt.Tx) error {
		bk := boltBucketsOf(tx)
		// the slice returned by Get points into the memory-mapped database and
		// is only valid for the life of the transaction, so copy it out
		if v := bk.chambers.Get([]byte(path)); v != nil {
			file = bytes.Clone(v)
			version = bk.version([]byte(path))
		}
		return nil
	})
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	if file == nil {
		err := &NotFoundError{logicalPath}
		recordError(span, err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: file, Version: version}, nil
}

func (b *BoltStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Put", trace.WithAttributes(attribute.String("realm.bolt.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}

	path := path.Clean(e.Key)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	value := e.Value
	if value == nil {
		// a nil Value deletes in a transaction; Put always stores
		value = []byte{}
	}
	err := b.db.Update(func(tx *bolt.Tx) error {
		return boltBucketsOf(tx).apply([]Op{{Key: path, Value: value}})
	})
	if err != nil {
		recordError(span, err)
		return err
	}

	return nil
}

func (b *BoltStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Delete", trace.WithAttributes(attribute.String("realm.bolt.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}

	path := path.Clean(logicalPath)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		bk := boltBucketsOf(tx)
		if bk.chambers.Get([]byte(path)) == nil {
			return &NotFoundError{logicalPath}
		}
		return bk.apply([]Op{{Key: path}})
	})

	if err != nil {
		recordError(span, err)
		return err
	}

	return nil
}

func (b *BoltStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := b.tracer.Start(ctx, "BoltStorage List", trace.WithAttributes(attribute.String("realm.bolt.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := path.Clean(prefix)
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	names := make([]string, 0)
	if err := b.db.View(func(tx *bolt.Tx) error {
		// Assume bucket exists and has keys
		c := tx.Bucket(chambersBucket).Cursor()

		cleanPrefix := []byte(path)
		set := make(map[string]struct{})
		for k, _ := c.Seek(cleanPrefix); k != nil && bytes.HasPrefix(k, cleanPrefix); k, _ = c.Next() {
			key := bytes.TrimPrefix(k, cleanPrefix)
			before, _, _ := bytes.Cut(key, []byte("/"))
			if len(before) > 0 {
				set[string(before)] = struct{}{}
			}
		}

		for key := range set {
			names = append(names, key)
		}

		return nil
	}); err != nil {
		recordError(span, err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	if len(names) > 0 {
		slices.Sort(names)
	}

	return names, nil
}

// Txn applies ops in one bbolt transaction if every check holds, giving
// every key it writes the next revision. A check fails, and nothing is
// applied, unless its key is at the checked version (0: absent).
func (b *BoltStorage) Txn(ctx context.Context, checks []Check, ops []Op) error {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Txn", trace.WithAttributes(attribute.Int("realm.bolt.checks", len(checks)), attribute.Int("realm.bolt.ops", len(ops))))
	defer span.End()

	for _, k := range append(checkKeys(checks), opKeys(ops)...) {
		if err := ValidatePath(k); err != nil {
			recordError(span, err)
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		recordError(span, err)
		return err
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		bk := boltBucketsOf(tx)
		for _, c := range checks {
			if bk.version([]byte(path.Clean(c.Key))) != c.Version {
				return ErrConflict
			}
		}
		cleaned := make([]Op, len(ops))
		for i, op := range ops {
			cleaned[i] = Op{Key: path.Clean(op.Key), Value: op.Value}
		}
		return bk.apply(cleaned)
	})
	if err != nil && !errors.Is(err, ErrConflict) {
		recordError(span, err)
	}
	return err
}

func (b *BoltStorage) Close(ctx context.Context) error {
	return b.db.Close()
}

// boltBuckets are the buckets of one bbolt transaction.
type boltBuckets struct {
	chambers, versions, meta *bolt.Bucket
}

func boltBucketsOf(tx *bolt.Tx) boltBuckets {
	return boltBuckets{
		chambers: tx.Bucket(chambersBucket),
		versions: tx.Bucket(versionsBucket),
		meta:     tx.Bucket(metaBucket),
	}
}

// version returns the revision of the entry at the cleaned key, or 0 when
// there is no entry.
func (bk boltBuckets) version(key []byte) uint64 {
	if bk.chambers.Get(key) == nil {
		return 0
	}
	v := bk.versions.Get(key)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

// nextRevision advances the revision counter and returns its new value.
func (bk boltBuckets) nextRevision() ([]byte, error) {
	var rev uint64
	if v := bk.meta.Get(revisionKey); len(v) == 8 {
		rev = binary.BigEndian.Uint64(v)
	}
	next := binary.BigEndian.AppendUint64(nil, rev+1)
	return next, bk.meta.Put(revisionKey, next)
}

// apply writes ops, whose keys are cleaned, at the next revision; a later op
// on a key replaces an earlier one. It takes no revision when there are no ops.
func (bk boltBuckets) apply(ops []Op) error {
	if len(ops) == 0 {
		return nil
	}
	rev, err := bk.nextRevision()
	if err != nil {
		return err
	}
	for _, op := range ops {
		key := []byte(op.Key)
		if op.Value == nil {
			if err := errors.Join(bk.chambers.Delete(key), bk.versions.Delete(key)); err != nil {
				return err
			}
			continue
		}
		if err := errors.Join(bk.chambers.Put(key, op.Value), bk.versions.Put(key, rev)); err != nil {
			return err
		}
	}
	return nil
}

// upgradeVersions gives every entry without a revision, such as those written
// before versions existed, one new revision, and drops the revisions of
// entries that no longer exist. It writes nothing when every entry has one.
func upgradeVersions(bk boltBuckets) error {
	var rev []byte
	c := bk.chambers.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if len(bk.versions.Get(k)) == 8 {
			continue
		}
		if rev == nil {
			var err error
			if rev, err = bk.nextRevision(); err != nil {
				return err
			}
		}
		if err := bk.versions.Put(bytes.Clone(k), rev); err != nil {
			return err
		}
	}

	var orphans [][]byte
	c = bk.versions.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if bk.chambers.Get(k) == nil {
			orphans = append(orphans, bytes.Clone(k))
		}
	}
	for _, k := range orphans {
		if err := bk.versions.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
