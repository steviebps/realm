package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
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
	_ Storage      = (*FileStorage)(nil)
	_ Locker       = (*FileStorage)(nil)
	_ KeyValidator = (*FileStorage)(nil)
)

const fileEntryKey string = "entry.json"

// internalPrefix begins the names of the files the backend keeps for itself
// (the lock file and in-flight writes), which List does not report.
const internalPrefix = ".realm-"

const (
	lockFileName = internalPrefix + "lock"
	tempPattern  = internalPrefix + "tmp-*"
	// tempNameBytes bounds the length of a temporary file's name: os.CreateTemp
	// replaces the "*" with at most 10 digits.
	tempNameBytes = len(tempPattern) - 1 + 10
)

// maxNameBytes is the longest file or directory name the file backend
// writes: the NAME_MAX of common file systems (ext4, XFS, APFS; NTFS counts
// UTF-16 units, and a name of 255 bytes has no more than 255 of them).
const maxNameBytes = 255

func NewFileStorage(conf map[string]string) (Storage, error) {
	if conf["path"] == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	return &FileStorage{
		path:   conf["path"],
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

// ValidateKey implements KeyValidator: it reports, wrapping ErrInvalidPath,
// why key cannot be stored beneath this store's directory (see
// validateFileKey). Every operation checks its key first, so a refused key
// never reaches the file system.
func (f *FileStorage) ValidateKey(key string) error {
	return validateFileKey(f.path, key)
}

// validateFileKey reports why key cannot be stored by the file backend in the
// directory root, wrapping ErrInvalidPath, or returns nil. A key is refused
// when:
//
//   - it references a parent (see ValidatePath);
//   - it contains a backslash, colon, or NUL byte, or one of its segments ends
//     in a dot or a space. Windows treats "\" as a separator and ":" as a
//     drive or stream separator, and drops trailing dots and spaces, so such
//     a key could name another key's files, including realm's reserved
//     metadata. These are refused on every platform, so that a store means
//     the same wherever it is opened, including on NTFS or SMB volumes;
//   - one of its segments begins with internalPrefix, which names the
//     backend's own files;
//   - a file or directory name it maps to is longer than maxNameBytes, or its
//     path (or that of the temporary file written beside it) is longer than
//     the platform accepts (see maxPathBytes).
func validateFileKey(root, key string) error {
	if err := ValidatePath(key); err != nil {
		return err
	}
	if i := strings.IndexAny(key, "\\:\x00"); i >= 0 {
		return fmt.Errorf("%w: %q contains %q, which file storage does not allow", ErrInvalidPath, key, key[i])
	}
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "" {
			continue
		}
		if last := seg[len(seg)-1]; last == '.' || last == ' ' {
			return fmt.Errorf("%w: segment %q of %q ends in %q, which file storage does not allow", ErrInvalidPath, seg, key, last)
		}
		if strings.HasPrefix(seg, internalPrefix) {
			return fmt.Errorf("%w: segment %q of %q begins with %q, which file storage keeps for its own files", ErrInvalidPath, seg, key, internalPrefix)
		}
	}

	// the names expandPath maps the key to: its directories, then the file
	dir, name := path.Split(path.Clean("/" + key + fileEntryKey))
	for seg := range strings.SplitSeq(strings.Trim(dir, "/"), "/") {
		if len(seg) > maxNameBytes {
			return fmt.Errorf("%w: segment %q of %q is longer than %d bytes", ErrInvalidPath, seg[:16]+"…", key, maxNameBytes)
		}
	}
	if name = "_" + name; len(name) > maxNameBytes {
		return fmt.Errorf("%w: the last segment of %q is longer than %d bytes", ErrInvalidPath, key, maxNameBytes-len("_"+fileEntryKey))
	}
	if n := len(filepath.Join(root, filepath.FromSlash(dir))) + 1 + max(len(name), tempNameBytes); n > maxPathBytes() {
		return fmt.Errorf("%w: %q makes a file path of %d bytes, longer than the %d this platform allows", ErrInvalidPath, key, n, maxPathBytes())
	}
	return nil
}

// maxPathBytes is the longest path, in bytes, that the platform's file calls
// accept: PATH_MAX less its terminating NUL. On Windows, Go lifts MAX_PATH by
// prefixing long paths with \\?\, which allows 32,767 UTF-16 units.
func maxPathBytes() int {
	switch runtime.GOOS {
	case "linux", "android":
		return 4095
	case "windows":
		return 32000
	default: // macOS and the BSDs
		return 1023
	}
}

func (f *FileStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := f.tracer.Start(ctx, "FileStorage Get", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()

	f.RLock()
	defer f.RUnlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := f.ValidateKey(logicalPath); err != nil {
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

	if err := f.ValidateKey(e.Key); err != nil {
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
	if err := mkdirAllSync(path); err != nil {
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

// mkdirAllSync creates dir and any missing parents, like os.MkdirAll, then
// syncs the parent of each directory it created, so the new directories
// survive a crash along with the entry written into them.
func mkdirAllSync(dir string) error {
	var created []string
	for d := filepath.Clean(dir); ; {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		created = append(created, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// outermost first, so each directory's entry is durable before the
	// directories inside it
	for _, d := range slices.Backward(created) {
		if err := syncDir(filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

func (f *FileStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := f.tracer.Start(ctx, "FileStorage Delete", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()

	f.Lock()
	defer f.Unlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := f.ValidateKey(logicalPath); err != nil {
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
	// sync the directory whose entries changed last, so the removal survives
	// a crash (another process may have removed it meanwhile)
	if err := syncDir(f.pruneEmptyDirs(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		recordError(span, err)
		return err
	}

	return nil
}

// pruneEmptyDirs removes dir and then each parent directory that is left
// empty, up to (not including) the storage root, so a deleted chamber with
// nothing below it no longer lists as a folder. It stops at the first
// directory that is not empty, and returns it: the deepest directory left,
// whose entries changed last. Failures to remove are ignored, since an empty
// directory left behind is harmless.
func (f *FileStorage) pruneEmptyDirs(dir string) string {
	root := filepath.Clean(f.path)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return dir
		}
	}
	return dir
}

func (f *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := f.tracer.Start(ctx, "FileStorage List", trace.WithAttributes(attribute.String("realm.file.prefix", prefix)))
	defer span.End()

	f.RLock()
	defer f.RUnlock()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := f.ValidateKey(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := f.path
	if prefix != "" {
		path = filepath.Join(path, prefix)
	}

	// Another process may delete entries (and prune their directories) while
	// this one lists: ReadDir skips entries that vanish before it reads their
	// type, and so does the stat below.
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		recordError(span, err)
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, internalPrefix) {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&fs.ModeSymlink != 0 {
			// follow links, as the entries they lead to are read through them
			fi, err := os.Stat(filepath.Join(path, name))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				recordError(span, err)
				return nil, err
			}
			isDir = fi.IsDir()
		}
		if isDir {
			names = append(names, name+"/")
		} else {
			names = append(names, ".")
		}
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	slices.Sort(names)
	// every file in a directory is the entry at its path
	return slices.Compact(names), nil
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
