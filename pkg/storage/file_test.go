package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

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
