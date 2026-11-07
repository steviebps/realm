package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestValidateFileKey checks which keys the file backend refuses before it
// touches the file system, on every platform: keys that Windows would map
// onto other keys' files (realm's reserved metadata among them), and names
// longer than the file system allows.
func TestValidateFileKey(t *testing.T) {
	const root = "/srv/realm"
	long := func(n int) string { return strings.Repeat("a", n) }
	tooDeep := strings.Repeat("/"+long(200), maxPathBytes()/200+2) + "/"
	tests := []struct {
		key string
		ok  bool
	}{
		{"/", true},
		{"", true},
		{"/a/b/", true},
		{"/_realm/history/%2Fa%2Fb%2F/12/", true},
		{"/a b/c/", true},
		{"/v1.2/.well-known/", true},
		{"/" + long(maxNameBytes) + "/", true},
		{"/a/" + long(maxNameBytes-len("_"+fileEntryKey)), true}, // without a trailing slash, the file is "_<last>entry.json"

		{"/a/../b/", false},
		{"/_realm\\keys/", false},
		{"/a\\b/", false},
		{"/_realm::$INDEX_ALLOCATION/keys/", false},
		{"/a:b/", false},
		{"/C:/x/", false},
		{"/a\x00b/", false},
		{"/_realm./keys/", false},
		{"/_realm /keys/", false},
		{"/a./", false},
		{"/a /", false},
		{"/.realm-lock/", false},
		{"/a/.realm-tmp-1/", false},
		{"/" + long(maxNameBytes+1) + "/", false},
		{"/a/" + long(maxNameBytes-len("_"+fileEntryKey)+1), false},
		{tooDeep, false},
	}
	for _, tt := range tests {
		name := tt.key
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		t.Run(name, func(t *testing.T) {
			err := validateFileKey(root, tt.key)
			if tt.ok && err != nil {
				t.Errorf("validateFileKey(%q) = %v, want nil", tt.key, err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidPath) {
				t.Errorf("validateFileKey(%q) = %v, want an error wrapping ErrInvalidPath", tt.key, err)
			}
		})
	}
}

// TestFileRefusesInvalidKeys checks that every operation of the file backend
// refuses an invalid key before it touches the file system.
func TestFileRefusesInvalidKeys(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := NewFileStorage(map[string]string{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"/_realm./keys/", "/_realm\\keys/", "/" + strings.Repeat("a", maxNameBytes+1) + "/"} {
		ops := map[string]error{
			"Put":    s.Put(ctx, StorageEntry{Key: key, Value: json.RawMessage(`{}`)}),
			"Delete": s.Delete(ctx, key),
		}
		_, ops["Get"] = s.Get(ctx, key)
		_, ops["List"] = s.List(ctx, key)
		for op, err := range ops {
			if !errors.Is(err, ErrInvalidPath) {
				t.Errorf("%s(%q) = %v, want an error wrapping ErrInvalidPath", op, key, err)
			}
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) > 0 {
		t.Errorf("refused keys touched the store: %v, %v", entries, err)
	}
}

// TestFileListToleratesConcurrentRemoval lists a directory while another
// process removes the entries in it: entries that vanish mid-listing are left
// out rather than failing the List.
func TestFileListToleratesConcurrentRemoval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := NewFileStorage(map[string]string{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	child := func(i int) string { return filepath.Join(root, "p", fmt.Sprintf("c%d", i)) }
	const n = 200
	for range 10 {
		// written directly, as another process would, without syncing
		for i := range n {
			if err := os.MkdirAll(child(i), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(child(i), "_"+fileEntryKey), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		removed := make(chan struct{})
		go func() {
			defer close(removed)
			for i := range n {
				_ = os.RemoveAll(child(i))
			}
		}()
		for listing := true; listing; {
			select {
			case <-removed:
				listing = false
			default:
			}
			if _, err := s.List(ctx, "/p/"); err != nil {
				<-removed
				t.Fatalf("List while entries were removed = %v", err)
			}
		}
	}
}

// TestFileDirectorySync checks the directories the file backend syncs:
// mkdirAllSync creates (and syncs the parents of) missing directories, and
// pruneEmptyDirs returns the deepest directory left after pruning, which
// Delete syncs so the removal is durable.
func TestFileDirectorySync(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	for range 2 { // missing, then existing
		if err := mkdirAllSync(deep); err != nil {
			t.Fatalf("mkdirAllSync(%s) = %v", deep, err)
		}
		if fi, err := os.Stat(deep); err != nil || !fi.IsDir() {
			t.Fatalf("after mkdirAllSync, %s: %v, %v", deep, fi, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mkdirAllSync(filepath.Join(root, "file", "x")); err == nil {
		t.Error("mkdirAllSync through a file succeeded")
	}

	if err := os.MkdirAll(filepath.Join(root, "a", "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &FileStorage{path: root}
	for _, tt := range []struct{ prune, want string }{
		{filepath.Join(root, "a", "b", "c"), filepath.Join(root, "a")}, // c and b go; a still holds d
		{filepath.Join(root, "a", "d"), filepath.Clean(root)},          // d, then the emptied a, go
	} {
		if got := f.pruneEmptyDirs(tt.prune); got != tt.want {
			t.Errorf("pruneEmptyDirs(%s) = %s, want %s", tt.prune, got, tt.want)
		}
	}
}

// fileWorkerEnv makes the test binary act as one of the processes in
// TestFileUpdatesAcrossProcesses; its value is the storage directory.
const fileWorkerEnv = "REALM_FILE_STORAGE_WORKER_DIR"

const (
	fileWorkers          = 4
	fileWorkerIncrements = 50
	counterKey           = "/counter/"
)

// TestFileUpdatesAcrossProcesses runs several processes that each increment
// one counter through Update while reading it back: every increment must
// survive, and no read may see a partially written file.
func TestFileUpdatesAcrossProcesses(t *testing.T) {
	if dir := os.Getenv(fileWorkerEnv); dir != "" {
		if err := incrementCounter(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	dir := t.TempDir()
	cmds := make([]*exec.Cmd, 0, fileWorkers)
	outs := make([]*strings.Builder, 0, fileWorkers)
	for range fileWorkers {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFileUpdatesAcrossProcesses$")
		cmd.Env = append(os.Environ(), fileWorkerEnv+"="+dir)
		out := &strings.Builder{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatalf("start worker: %v", err)
		}
		cmds = append(cmds, cmd)
		outs = append(outs, out)
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("worker %d: %v\n%s", i, err, outs[i])
		}
	}

	s, err := NewFileStorage(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if _, err := ReadJSON(context.Background(), s, counterKey, &got); err != nil {
		t.Fatal(err)
	}
	if want := fileWorkers * fileWorkerIncrements; got != want {
		t.Errorf("counter = %d after %d increments: updates from different processes were lost", got, want)
	}

	// the lock file and temporary files are not entries
	names, err := s.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"counter/"}; !slices.Equal(names, want) {
		t.Errorf("List(/) = %v, want %v", names, want)
	}
}

func incrementCounter(dir string) error {
	ctx := context.Background()
	s, err := NewFileStorage(map[string]string{"path": dir})
	if err != nil {
		return err
	}
	for range fileWorkerIncrements {
		if _, err := UpdateJSON(ctx, s, counterKey, func(n *int) error {
			*n++
			return nil
		}); err != nil {
			return fmt.Errorf("update: %w", err)
		}
		// a plain read, which takes no lock, must never see a partial write
		e, err := s.Get(ctx, counterKey)
		if err != nil {
			return fmt.Errorf("get: %w", err)
		}
		var n int
		if err := json.Unmarshal(e.Value, &n); err != nil {
			return fmt.Errorf("read a partial entry %q: %w", e.Value, err)
		}
	}
	return nil
}

func TestFileLockUpdateHonorsContext(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewFileStorage(map[string]string{"path": dir})
	// a second instance stands in for another process: it shares the lock
	// file but not the first instance's in-process mutex
	b, _ := NewFileStorage(map[string]string{"path": dir})

	unlock, err := a.(Locker).LockUpdate(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.(Locker).LockUpdate(ctx, nil); err == nil {
		t.Fatal("expected LockUpdate to give up when its context is done while the store is locked")
	}
	unlock()

	unlock, err = b.(Locker).LockUpdate(context.Background(), nil)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock()
}

func TestBoltOpenWhileLockedFails(t *testing.T) {
	dir := t.TempDir()
	first, err := NewBoltStorage(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())

	// before boltLockTimeout, this waited for the lock forever
	_, err = NewBoltStorage(map[string]string{"path": dir})
	if err == nil || !strings.Contains(err.Error(), "locked by another process") {
		t.Fatalf("expected a locked-database error, got %v", err)
	}
}

func TestCacheableRejectsBoltOverBolt(t *testing.T) {
	_, err := NewCacheableStorageWithConf(map[string]string{"cache": "boltdb", "source": "boltdb", "path": t.TempDir()})
	if err == nil {
		t.Fatal("expected boltdb over boltdb to be rejected")
	}
}

// TestFileDeletePrunesEmptyDirs checks that deleting a chamber removes the
// directories it leaves empty, so the path stops listing as a folder, while
// directories that still hold chambers are kept.
func TestFileDeletePrunesEmptyDirs(t *testing.T) {
	ctx := context.Background()
	s, err := NewFileStorage(map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"/a/", "/a/b/c/", "/x/y/"} {
		if err := s.Put(ctx, StorageEntry{Key: k, Value: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	steps := []struct {
		del      string
		listPath string
		want     []string
	}{
		{del: "/a/b/c/", listPath: "/a/", want: []string{"."}}, // b/ and c/ pruned, a's own entry kept
		{del: "/x/y/", listPath: "/", want: []string{"a/"}},    // x/ pruned up to the root
		{del: "/a/", listPath: "/", want: nil},                 // last chamber gone
	}
	for _, st := range steps {
		if err := s.Delete(ctx, st.del); err != nil {
			t.Fatalf("delete %s: %v", st.del, err)
		}
		got, err := s.List(ctx, st.listPath)
		if err != nil {
			t.Fatalf("list %s: %v", st.listPath, err)
		}
		if !slices.Equal(got, st.want) {
			t.Errorf("after deleting %s, List(%s) = %v, want %v", st.del, st.listPath, got, st.want)
		}
	}
}
