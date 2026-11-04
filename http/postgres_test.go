package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// newReplicas starts n servers that share one PostgreSQL database, as replicas
// behind a load balancer would.
func newReplicas(t *testing.T, n int) []*httptest.Server {
	t.Helper()
	url := pgtest.URL(t)
	servers := make([]*httptest.Server, n)
	for i := range servers {
		pg, err := storage.NewPostgresStorage(map[string]string{"url": url})
		if err != nil {
			t.Fatalf("postgres: %v", err)
		}
		inh, err := storage.NewInheritableStorage(pg)
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
			_ = pg.Close(context.Background())
		})
	}
	return servers
}

func TestReplicasStreamEachOthersWrites(t *testing.T) {
	a, b := pair(t)
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
	// well within the change feed's poll interval, so NOTIFY delivered it
	if next := waitData(t, events, 2*time.Second); !strings.Contains(next, `"value":true`) {
		t.Fatalf("update event = %s", next)
	}
}

func TestReplicasEnforceIfMatchTogether(t *testing.T) {
	a, b := pair(t)
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
	a, b := pair(t)

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

func pair(t *testing.T) (*httptest.Server, *httptest.Server) {
	t.Helper()
	s := newReplicas(t, 2)
	return s[0], s[1]
}
