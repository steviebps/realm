package http

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// revisionOf returns the revision header of a response, failing the test when
// it is not a positive integer.
func revisionOf(t *testing.T, resp *http.Response) uint64 {
	t.Helper()
	h := resp.Header.Get(api.RevisionHeader)
	rev, err := strconv.ParseUint(h, 10, 64)
	if err != nil || rev == 0 {
		t.Fatalf("%s = %q, want a revision", api.RevisionHeader, h)
	}
	return rev
}

// newRevisionTestServer serves an inheriting handler over boltdb, which
// tracks revisions.
func newRevisionTestServer(t *testing.T) string {
	t.Helper()
	bolt, err := storage.NewBoltStorage(map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bolt.Close(context.Background()) })
	return newInheritableTestServerOn(t, bolt).URL
}

// TestChamberReadsCarryRevisions verifies that chamber reads and evaluations
// report the revision of the inherited view: the highest among the chamber
// and its parents, so a parent's write changes it.
func TestChamberReadsCarryRevisions(t *testing.T) {
	base := newRevisionTestServer(t)
	chamber := base + "/v1/chambers/"
	put := func(path, value string) {
		t.Helper()
		if resp, out := do(t, http.MethodPost, chamber+path, fmt.Sprintf(flagBody, value), nil); resp.StatusCode != http.StatusCreated {
			t.Fatalf("put %s: %d %v", path, resp.StatusCode, out.Errors)
		}
	}
	get := func(url string, headers map[string]string) *http.Response {
		t.Helper()
		resp, out := do(t, http.MethodGet, url, "", headers)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get %s: %d %v", url, resp.StatusCode, out.Errors)
		}
		return resp
	}

	put("a/", "true")
	put("", "false")
	own := revisionOf(t, get(chamber+"a/?inherit=false", nil))
	inherited := revisionOf(t, get(chamber+"a/", nil))
	if inherited <= own {
		t.Errorf("inherited revision %d, want above the chamber's own %d: the root was written after it", inherited, own)
	}
	if root := revisionOf(t, get(chamber, nil)); root != inherited {
		t.Errorf("root revision %d, want the child's inherited %d", root, inherited)
	}

	put("", "true")
	after := revisionOf(t, get(chamber+"a/", nil))
	if after <= inherited {
		t.Errorf("after a parent write the revision is %d, want above %d", after, inherited)
	}
	if got := revisionOf(t, get(chamber+"a/?inherit=false", nil)); got != own {
		t.Errorf("the chamber's own revision moved from %d to %d without a write to it", own, got)
	}

	// the evaluation endpoint reports it too, readable by pages elsewhere
	resp, out := do(t, http.MethodPost, base+"/v1/evaluate/a/", `{"key":"u"}`, map[string]string{"Origin": "https://app.example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("evaluate: %d %v", resp.StatusCode, out.Errors)
	}
	if got := revisionOf(t, resp); got != after {
		t.Errorf("evaluation revision %d, want %d", got, after)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); !strings.Contains(got, api.RevisionHeader) {
		t.Errorf("Access-Control-Expose-Headers = %q, want it to include %s", got, api.RevisionHeader)
	}
}

// TestNoRevisionWithoutTransactionalStorage verifies that storage that does
// not track revisions sends no revision header.
func TestNoRevisionWithoutTransactionalStorage(t *testing.T) {
	srv := newInheritableTestServer(t)
	if resp, _ := do(t, http.MethodPost, srv.URL+"/v1/chambers/a/", fmt.Sprintf(flagBody, "true"), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	resp, _ := do(t, http.MethodGet, srv.URL+"/v1/chambers/a/", "", nil)
	if h, ok := resp.Header[api.RevisionHeader]; ok {
		t.Errorf("%s = %q, want none", api.RevisionHeader, h)
	}
}

// sseEvent is one server-sent event's id and data.
type sseEvent struct {
	id, data string
}

// readSSE reads an SSE body, emitting each event's id and data.
func readSSE(scanner *bufio.Scanner) <-chan sseEvent {
	out := make(chan sseEvent)
	go func() {
		defer close(out)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if ev.data != "" {
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

// TestStreamEventsCarryRevisions verifies that each event of a chamber stream
// and an evaluation stream has the revision it was read at as its id.
func TestStreamEventsCarryRevisions(t *testing.T) {
	base := newRevisionTestServer(t)
	chamber := base + "/v1/chambers/a/"
	put := func(value string) uint64 {
		t.Helper()
		resp, out := do(t, http.MethodPost, chamber, fmt.Sprintf(flagBody, value), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("put: %d %v", resp.StatusCode, out.Errors)
		}
		resp, _ = do(t, http.MethodGet, chamber, "", nil)
		return revisionOf(t, resp)
	}
	first := put("true")

	streams := map[string]func() (*http.Response, error){
		"chamber": func() (*http.Response, error) {
			return http.Get(chamber + "?watch=true")
		},
		"evaluation": func() (*http.Response, error) {
			return http.Post(base+"/v1/evaluate/a/?watch=true", "application/json", strings.NewReader(`{"key":"u"}`))
		},
	}
	events := map[string]<-chan sseEvent{}
	for name, open := range streams {
		resp, err := open()
		if err != nil {
			t.Fatalf("%s stream: %v", name, err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		events[name] = readSSE(bufio.NewScanner(resp.Body))
	}
	next := func(name string) sseEvent {
		t.Helper()
		select {
		case ev, ok := <-events[name]:
			if !ok {
				t.Fatalf("%s stream closed", name)
			}
			return ev
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for a %s event", name)
			return sseEvent{}
		}
	}
	for name := range streams {
		if ev := next(name); ev.id != strconv.FormatUint(first, 10) {
			t.Errorf("%s: first event id %q, want %d", name, ev.id, first)
		}
	}

	second := put("false")
	for name := range streams {
		if ev := next(name); ev.id != strconv.FormatUint(second, 10) {
			t.Errorf("%s: event id after a write %q, want %d", name, ev.id, second)
		}
	}
}

// TestSDKReportsRevision verifies that the Go SDK exposes the revision of its
// snapshot, polled or streamed, and that a context keeps the revision of the
// snapshot it pinned.
func TestSDKReportsRevision(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			base := newRevisionTestServer(t)
			chamber := base + "/v1/chambers/a/"
			put := func(value string) uint64 {
				t.Helper()
				resp, out := do(t, http.MethodPost, chamber, fmt.Sprintf(flagBody, value), nil)
				if resp.StatusCode != http.StatusCreated {
					t.Fatalf("put: %d %v", resp.StatusCode, out.Errors)
				}
				resp, _ = do(t, http.MethodGet, chamber, "", nil)
				return revisionOf(t, resp)
			}
			first := put("true")

			c, err := client.NewHttpClient(&client.HttpClientConfig{Address: base})
			if err != nil {
				t.Fatal(err)
			}
			rlm, err := realm.NewRealm(realm.WithHttpClient(c), realm.WithPath("/a/"), realm.WithStreaming(streaming), realm.WithPollingInterval(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if got := rlm.Revision(ctx); got != 0 {
				t.Errorf("revision before Start = %d, want 0", got)
			}
			if err := rlm.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(rlm.Stop)
			if got := rlm.Revision(ctx); got != first {
				t.Errorf("revision = %d, want %d", got, first)
			}
			pinned := rlm.NewContext(ctx)
			if got := rlm.Revision(pinned); got != first {
				t.Errorf("pinned revision = %d, want %d", got, first)
			}

			second := put("false")
			deadline := time.Now().Add(5 * time.Second)
			for rlm.Revision(ctx) != second && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := rlm.Revision(ctx); got != second {
				t.Errorf("revision after a write = %d, want %d", got, second)
			}
			if got := rlm.Revision(pinned); got != first {
				t.Errorf("a pinned context's revision moved to %d, want %d", got, first)
			}
			if v, err := rlm.Bool(pinned, "beta", false); err != nil || !v {
				t.Errorf("pinned beta = %v, %v; want true", v, err)
			}
		})
	}
}
