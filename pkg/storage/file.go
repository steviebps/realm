package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FileStorage keeps each entry in its own file beneath a directory. Writes
// replace files atomically, so other processes reading the directory never see a
// partial entry, and Updates
// are serialized across processes with a lock file (see LockUpdate).
type FileStorage struct {
	sync.RWMutex
	// updateMu queues this process's Updates before they take the lock file,
	// so they wait on each other without polling it.
	updateMu sync.Mutex
	path     string
	tracer   trace.Tracer
}

var (
	_ Storage = (*FileStorage)(nil)
	_ Locker  = (*FileStorage)(nil)
)

const fileEntryKey string = "entry.json"

// internalPrefix begins the names of the files the backend keeps for itself
// (the lock file and in-flight writes), which List does not report.
const internalPrefix = ".realm-"

const (
	lockFileName = internalPrefix + "lock"
	tempPattern  = internalPrefix + "tmp-*"
)

func NewFileStorage(conf map[string]string) (Storage, error) {
	if conf["path"] == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	return &FileStorage{
		path:   conf["path"],
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (f *FileStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := f.tracer.Start(ctx, "FileStorage Get", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()

	f.RLock()
	defer f.RUnlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}

	path, key := f.expandPath(logicalPath + fileEntryKey)
	value, err := os.ReadFile(filepath.Join(path, key))
	if err != nil {
		recordError(span, err)
		if errors.Is(err, os.ErrNotExist) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: value}, nil
}

func (f *FileStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := f.tracer.Start(ctx, "FileStorage Put", trace.WithAttributes(attribute.String("realm.file.entry.key", e.Key)))
	defer span.End()

	f.Lock()
	defer f.Unlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}
	path, key := f.expandPath(e.Key + fileEntryKey)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	// Make the parent tree
	if err := os.MkdirAll(path, 0o700); err != nil {
		recordError(span, err)
		return err
	}

	// the value is stored byte for byte, so reads return exactly what was
	// written and ETags computed at write time match later reads
	if err := writeFileAtomic(path, key, e.Value); err != nil {
		recordError(span, err)
		return err
	}
	return nil
}

// writeFileAtomic replaces dir/name with data. The data goes to a temporary
// file in dir that is synced and then renamed over the target, so readers
// see the old contents or the new, never a partial write, and a crash cannot
// leave the entry truncated.
func writeFileAtomic(dir, name string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = replaceFile(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	// make the rename itself durable
	return syncDir(dir)
}

func (f *FileStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := f.tracer.Start(ctx, "FileStorage Delete", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()

	f.Lock()
	defer f.Unlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}
	path, key := f.expandPath(logicalPath + fileEntryKey)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	if err := os.Remove(filepath.Join(path, key)); err != nil {
		recordError(span, err)
		if errors.Is(err, os.ErrNotExist) {
			return &NotFoundError{logicalPath}
		}
		return err
	}
	f.pruneEmptyDirs(path)

	return nil
}

// pruneEmptyDirs removes dir and then each parent directory that is left
// empty, up to (not including) the storage root, so a deleted chamber with
// nothing below it no longer lists as a folder. It stops at the first
// directory that is not empty; failures are ignored, since an empty directory
// left behind is harmless.
func (f *FileStorage) pruneEmptyDirs(dir string) {
	root := filepath.Clean(f.path)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}

func (f *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := f.tracer.Start(ctx, "FileStorage List", trace.WithAttributes(attribute.String("realm.file.prefix", prefix)))
	defer span.End()

	f.RLock()
	defer f.RUnlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := f.path
	if prefix != "" {
		path = filepath.Join(path, prefix)
	}

	file, err := os.Open(path)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		recordError(span, err)
		return nil, err
	}

	names, err := file.Readdirnames(-1)
	if err != nil {
		recordError(span, err)
		return nil, err
	}

	names = slices.DeleteFunc(names, func(name string) bool {
		return strings.HasPrefix(name, internalPrefix)
	})
	for i, name := range names {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			names[i] = name + "/"
		} else {
			names[i] = "."
		}
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

func (f *FileStorage) Close(ctx context.Context) error {
	return nil
}

// LockUpdate takes an exclusive lock on the whole store, shared with every
// process that opens the same directory, and returns a function that releases
// it. Updates hold it while they read and write, so processes sharing the
// directory cannot overwrite each other's changes.
func (f *FileStorage) LockUpdate(ctx context.Context, _ []string) (func(), error) {
	f.updateMu.Lock()
	unlock, err := f.lockFile(ctx)
	if err != nil {
		f.updateMu.Unlock()
		return nil, err
	}
	return func() {
		unlock()
		f.updateMu.Unlock()
	}, nil
}

// lockFile takes the lock file in the store's directory, polling with
// backoff until another process releases it or ctx is done.
func (f *FileStorage) lockFile(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(f.path, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(f.path, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for wait := time.Millisecond; ; wait = min(2*wait, 50*time.Millisecond) {
		locked, err := tryLockFile(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("could not lock %s: %w", file.Name(), err)
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	// closing the file releases the lock
	return func() { _ = file.Close() }, nil
}

func (f *FileStorage) expandPath(k string) (string, string) {
	path := filepath.Join(f.path, k)
	key := filepath.Base(path)
	path = filepath.Dir(path)
	return path, "_" + key
}
