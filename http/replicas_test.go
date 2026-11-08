package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/pkg/storage/etcdtest"
	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// sharedBackends maps every locally testable backend that several servers
// can share to a function that prepares fresh, empty data for a test and
// returns a function opening one server's storage over it.
func sharedBackends(t *testing.T) map[string]func(t *testing.T) func() (storage.Storage, error) {
	out := map[string]func(t *testing.T) func() (storage.Storage, error){}
	if os.Getenv(pgtest.EnvURL) != "" {
		out["postgres"] = func(t *testing.T) func() (storage.Storage, error) {
			url := pgtest.URL(t)
			return func() (storage.Storage, error) { return storage.NewPostgresStorage(map[string]string{"url": url}) }
		}
	}
	if os.Getenv(etcdtest.EnvEndpoints) != "" {
		out["etcd"] = func(t *testing.T) func() (storage.Storage, error) {
			conf := etcdtest.Options(t)
			return func() (storage.Storage, error) { return storage.NewEtcdStorage(conf) }
		}
	}
	if len(out) == 0 {
		t.Skipf("set %s or %s to run replica tests", pgtest.EnvURL, etcdtest.EnvEndpoints)
	}
	return out
}

// forEachSharedBackend runs test for every backend several servers can
// share, with a pair of servers sharing fresh data, as replicas behind a
// load balancer would.
func forEachSharedBackend(t *testing.T, test func(t *testing.T, a, b *httptest.Server)) {
	for name, prepare := range sharedBackends(t) {
		t.Run(name, func(t *testing.T) {
			s := newReplicas(t, 2, prepare(t))
			test(t, s[0], s[1])
		})
	}
}

// newReplicas starts n servers whose storage open opens over shared data.
func newReplicas(t *testing.T, n int, open func() (storage.Storage, error)) []*httptest.Server {
	t.Helper()
	servers := make([]*httptest.Server, n)
	for i := range servers {
		strg, err := open()
		if err != nil {
			t.Fatalf("storage: %v", err)
		}
		inh, err := storage.NewInheritableStorage(strg)
		if err != nil {
			t.Fatalf("inheritable: %v", err)
		}
		h, err := NewHandler(context.Background(), HandlerConfig{Storage: inh})
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		servers[i] = httptest.NewServer(h)
		t.Cleanup(func() {
			servers[i].Close()
			_ = strg.Close(context.Background())
		})
	}
	return servers
}

func TestReplicasStreamEachOthersWrites(t *testing.T) {
	forEachSharedBackend(t, testReplicasStreamEachOthersWrites)
}

func testReplicasStreamEachOthersWrites(t *testing.T, a, b *httptest.Server) {
	putChamber(t, a.URL, "app", fmt.Sprintf(flagBody, "false"))
	putChamber(t, a.URL, "app/web", `{"rules":{}}`)

	// b streams the inherited view of a child; a changes its parent
	resp, err := http.Get(b.URL + "/v1/chambers/app/web?watch=true")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	events := dataEvents(bufio.NewScanner(resp.Body))
	if first := waitData(t, events, 2*time.Second); !strings.Contains(first, `"value":false`) {
		t.Fatalf("initial event = %s", first)
	}

	putChamber(t, a.URL, "app", fmt.Sprintf(flagBody, "true"))
	// well within any poll interval, so the change feed pushed it
	if next := waitData(t, events, 2*time.Second); !strings.Contains(next, `"value":true`) {
		t.Fatalf("update event = %s", next)
	}
}

func TestReplicasEnforceIfMatchTogether(t *testing.T) {
	forEachSharedBackend(t, testReplicasEnforceIfMatchTogether)
}

func testReplicasEnforceIfMatchTogether(t *testing.T, a, b *httptest.Server) {
	putChamber(t, a.URL, "app", fmt.Sprintf(flagBody, "false"))

	resp, _ := do(t, http.MethodGet, b.URL+"/v1/chambers/app?inherit=false", "", nil)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag from the other replica")
	}

	// both replicas try to replace the same version at once; one must lose
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i, srv := range []*httptest.Server{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := do(t, http.MethodPost, srv.URL+"/v1/chambers/app", fmt.Sprintf(`{"rules":{"beta":{"type":"boolean","value":true},"by":{"type":"number","value":%d}}}`, i), map[string]string{"If-Match": etag})
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	got := map[int]int{}
	for s := range statuses {
		got[s]++
	}
	if got[http.StatusCreated] != 1 || got[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("statuses = %v, want one 201 and one 412", got)
	}
}

func TestReplicasKeepCompleteHistory(t *testing.T) {
	forEachSharedBackend(t, testReplicasKeepCompleteHistory)
}

func testReplicasKeepCompleteHistory(t *testing.T, a, b *httptest.Server) {

	const perServer = 10
	var wg sync.WaitGroup
	for _, srv := range []*httptest.Server{a, b} {
		for i := range perServer {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, _ := do(t, http.MethodPost, srv.URL+"/v1/chambers/app", fmt.Sprintf(`{"rules":{"n":{"type":"number","value":%d}}}`, i), nil)
				if resp.StatusCode != http.StatusCreated {
					t.Errorf("POST: %d", resp.StatusCode)
				}
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, _ := do(t, http.MethodPatch, srv.URL+"/v1/catalog/tenant", fmt.Sprintf(`{"values":{"%s-%d":"label"}}`, srv.URL, i), nil)
				if resp.StatusCode != http.StatusOK {
					t.Errorf("PATCH catalog: %d", resp.StatusCode)
				}
			}()
		}
	}
	wg.Wait()

	// no revision or catalog value may be lost to a concurrent writer
	_, out := do(t, http.MethodGet, b.URL+"/v1/history/app", "", nil)
	var revs []Revision
	if err := json.Unmarshal(out.Data, &revs); err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(revs) != 2*perServer {
		t.Fatalf("history has %d revisions, want %d", len(revs), 2*perServer)
	}
	for i, r := range revs {
		if want := 2*perServer - i; r.Revision != want {
			t.Fatalf("revision %d is numbered %d, want %d", i, r.Revision, want)
		}
	}

	_, out = do(t, http.MethodGet, a.URL+"/v1/catalog/tenant", "", nil)
	var catalog struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(out.Data, &catalog); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(catalog.Values) != 2*perServer {
		t.Fatalf("catalog has %d values, want %d", len(catalog.Values), 2*perServer)
	}
}
