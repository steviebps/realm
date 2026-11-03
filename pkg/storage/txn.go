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
	// ErrConflict and applies nothing. It returns the revision that wrote the
	// ops. Deleting a key that does not exist is not an error.
	Txn(ctx context.Context, checks []Check, ops []Op) (uint64, error)
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

// maxUpdateAttempts bounds the retries of an Update that keeps conflicting.
const maxUpdateAttempts = 10

// Update atomically reads the entries at keys, passes them to fn (a missing
// entry is nil), and applies the ops fn returns. fn may run more than once, so
// it must not have side effects; an error from fn aborts the update and is
// returned as is.
//
// On a Transactional backend each read key is version-checked when the ops
// are applied, and fn is retried when another writer got there first; after
// repeated conflicts Update returns ErrConflict. Other backends are only safe
// within one process: Update holds a process-wide lock on the keys and
// applies the ops one at a time.
func Update(ctx context.Context, s Storage, keys []string, fn func(map[string]*StorageEntry) ([]Op, error)) error {
	for _, k := range keys {
		if err := ValidatePath(k); err != nil {
			return err
		}
	}
	if t, ok := s.(Transactional); ok {
		return updateTransactional(ctx, t, keys, fn)
	}
	unlock := localLocks.lock(keys)
	defer unlock()
	current, err := readEntries(ctx, s, keys)
	if err != nil {
		return err
	}
	ops, err := fn(current)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := ValidatePath(op.Key); err != nil {
			return err
		}
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
		if err != nil {
			return err
		}
		for _, op := range ops {
			if err := ValidatePath(op.Key); err != nil {
				return err
			}
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
		if _, err = t.Txn(ctx, checks, ops); !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

// readEntries gets each key, mapping missing entries to nil.
func readEntries(ctx context.Context, s Storage, keys []string) (map[string]*StorageEntry, error) {
	out := make(map[string]*StorageEntry, len(keys))
	for _, k := range keys {
		e, err := s.Get(ctx, k)
		var nf *NotFoundError
		if errors.As(err, &nf) {
			out[k] = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		out[k] = e
	}
	return out, nil
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
	for _, k := range keys {
		h := fnv.New32a()
		_, _ = h.Write([]byte(k))
		idx = append(idx, int(h.Sum32()%uint32(len(l.stripes))))
	}
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
