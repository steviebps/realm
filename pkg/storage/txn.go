package storage

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// ErrConflict is returned when a transaction's version check fails because
// another writer changed a key first.
var ErrConflict = errors.New("the entry was changed concurrently")

// Op is one change in a transaction: a put, or a delete when Value is nil.
type Op struct {
	Key   string
	Value json.RawMessage
}

// Check requires the entry at Key to be at Version when a transaction
// commits; 0 means the entry must not exist.
type Check struct {
	Key     string
	Version uint64
}

// Transactional is implemented by backends that apply several changes
// atomically and version every entry. Servers sharing such a backend stay
// consistent with each other, because Update checks versions in the backend
// rather than holding a lock in one process.
type Transactional interface {
	Storage
	// Txn applies ops atomically if every check holds, or returns
	// ErrConflict and applies nothing. Deleting a key that does not exist is
	// not an error.
	Txn(ctx context.Context, checks []Check, ops []Op) error
}

// Change reports that the entry at Key was written or deleted at revision
// Rev. A change to "/" may also mean that changes were missed, so anything
// may have changed.
type Change struct {
	Rev uint64
	Key string
}

// Watchable is implemented by backends that report every change, including
// those made by other servers sharing the backend.
type Watchable interface {
	Storage
	// Watch returns the changes committed after it is called, in revision
	// order, until ctx is done or the storage is closed.
	Watch(ctx context.Context) <-chan Change
}

// Locker is implemented by backends that are not Transactional but can
// serialize Updates across every process sharing them, such as the file
// backend's lock file.
type Locker interface {
	Storage
	// LockUpdate blocks until the caller alone may update keys, or ctx is
	// done, and returns a function that releases the lock.
	LockUpdate(ctx context.Context, keys []string) (unlock func(), err error)
}

// UpdateReader is implemented by storage whose reads may be served from a
// copy that lags the stored entries, such as cacheable's cache. Update reads
// the keys it changes through ReadForUpdate, so a read-modify-write never
// starts from a stale copy and writes it back.
type UpdateReader interface {
	Storage
	// ReadForUpdate returns the entry at key as stored, bypassing any copy.
	ReadForUpdate(ctx context.Context, key string) (*StorageEntry, error)
}

// KeyValidator is implemented by backends that cannot store every key, such
// as the file backend, whose file system limits the length of names. Update
// validates the key of every op before applying any, so a key the backend
// refuses fails the whole update rather than leaving earlier ops applied.
type KeyValidator interface {
	Storage
	// ValidateKey returns an error wrapping ErrInvalidPath when key cannot be
	// stored.
	ValidateKey(key string) error
}

// validateKeys checks the key of every op, when s is a KeyValidator.
func validateKeys(s Storage, ops []Op) error {
	v, ok := s.(KeyValidator)
	if !ok {
		return nil
	}
	for _, op := range ops {
		if err := v.ValidateKey(op.Key); err != nil {
			return err
		}
	}
	return nil
}

// maxUpdateAttempts bounds the retries of an Update that keeps conflicting.
const maxUpdateAttempts = 10

// Update atomically reads the entries at keys, passes them to fn (a missing
// entry is nil), and applies the ops fn returns. fn may run more than once, so
// it must not have side effects; an error from fn aborts the update and is
// returned as is.
//
// Update works on the entries as stored (see Raw), beneath any wrapper that
// transforms reads. On a Transactional backend each read key is
// version-checked when the ops are applied, and fn is retried when another
// writer got there first; after repeated conflicts Update returns
// ErrConflict. On other backends Update holds a process-wide lock on the keys,
// plus the backend's own lock when it is a Locker (which extends the guarantee
// to other processes on the same machine), and applies the ops one at a time.
// Either way it reads through UpdateReader when the storage implements it,
// and checks every op's key with KeyValidator before applying any.
func Update(ctx context.Context, s Storage, keys []string, fn func(map[string]*StorageEntry) ([]Op, error)) error {
	s = Raw(s)
	if t, ok := s.(Transactional); ok {
		return updateTransactional(ctx, t, keys, fn)
	}
	unlock := localLocks.lock(keys)
	defer unlock()
	if l, ok := s.(Locker); ok {
		release, err := l.LockUpdate(ctx, keys)
		if err != nil {
			return err
		}
		defer release()
	}
	current, err := readEntries(ctx, s, keys)
	if err != nil {
		return err
	}
	ops, err := fn(current)
	if err != nil {
		return err
	}
	if err := validateKeys(s, ops); err != nil {
		return err
	}
	for _, op := range ops {
		if op.Value == nil {
			err = s.Delete(ctx, op.Key)
			var nf *NotFoundError
			if errors.As(err, &nf) {
				err = nil
			}
		} else {
			err = s.Put(ctx, StorageEntry{Key: op.Key, Value: op.Value})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func updateTransactional(ctx context.Context, t Transactional, keys []string, fn func(map[string]*StorageEntry) ([]Op, error)) error {
	for attempt := range maxUpdateAttempts {
		if attempt > 0 {
			// back off with jitter, so contending writers spread out
			wait := time.Duration(rand.Int64N(int64(time.Millisecond << attempt)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		current, err := readEntries(ctx, t, keys)
		if err != nil {
			return err
		}
		ops, err := fn(current)
		if err != nil || len(ops) == 0 {
			return err
		}
		if err := validateKeys(t, ops); err != nil {
			return err
		}
		// version-check every key that was read, so fn's view still holds
		checks := make([]Check, 0, len(keys))
		for _, k := range keys {
			var v uint64
			if e := current[k]; e != nil {
				v = e.Version
			}
			checks = append(checks, Check{Key: k, Version: v})
		}
		if err = t.Txn(ctx, checks, ops); !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

// readEntries reads each key as stored (through UpdateReader when s
// implements it), mapping missing entries to nil.
func readEntries(ctx context.Context, s Storage, keys []string) (map[string]*StorageEntry, error) {
	get := s.Get
	if r, ok := s.(UpdateReader); ok {
		get = r.ReadForUpdate
	}
	out := make(map[string]*StorageEntry, len(keys))
	for _, k := range keys {
		e, err := optional(get(ctx, k))
		if err != nil {
			return nil, err
		}
		out[k] = e
	}
	return out, nil
}

// lockOrder returns a 32-bit hash of each key, sorted and without
// duplicates: the order in which locks on the keys are taken, so that
// transactions locking overlapping keys cannot deadlock.
func lockOrder(keys []string) []uint32 {
	ids := make([]uint32, 0, len(keys))
	for _, k := range keys {
		h := fnv.New32a()
		_, _ = h.Write([]byte(k))
		ids = append(ids, h.Sum32())
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// localLocks serializes Updates of the same keys within this process, for
// backends that are not Transactional. Locks are striped to bound memory.
var localLocks stripedLocks

type stripedLocks struct {
	stripes [64]sync.Mutex
}

// lock takes the stripes of keys in a fixed order, so updates of overlapping
// keys cannot deadlock, and returns a function that releases them.
func (l *stripedLocks) lock(keys []string) func() {
	idx := make([]int, 0, len(keys))
	for _, id := range lockOrder(keys) {
		idx = append(idx, int(id%uint32(len(l.stripes))))
	}
	// sorting again keeps the stripes ordered after the modulo
	slices.Sort(idx)
	idx = slices.Compact(idx)
	for _, i := range idx {
		l.stripes[i].Lock()
	}
	return func() {
		for _, i := range slices.Backward(idx) {
			l.stripes[i].Unlock()
		}
	}
}

// UpdateJSON atomically applies fn to the JSON value stored at key (the zero
// value when it does not exist), stores the result, and returns it. Like
// Update, fn may run more than once and must not have side effects.
func UpdateJSON[T any](ctx context.Context, s Storage, key string, fn func(*T) error) (T, error) {
	var out T
	err := Update(ctx, s, []string{key}, func(cur map[string]*StorageEntry) ([]Op, error) {
		var v T
		if e := cur[key]; e != nil {
			if err := json.Unmarshal(e.Value, &v); err != nil {
				return nil, err
			}
		}
		if err := fn(&v); err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out = v
		return []Op{{Key: key, Value: b}}, nil
	})
	return out, err
}
