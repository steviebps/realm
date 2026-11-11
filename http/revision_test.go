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
// tracks revisions, and returns its URL.
func newRevisionTestServer(t *testing.T) string {
	t.Helper()
	return newInheritableTestServerOn(t, deleteBackends["boltdb"](t)).URL
}

// getRevision reads the chamber at url and returns its revision.
func getRevision(t *testing.T, url string) uint64 {
	t.Helper()
	resp, out := do(t, http.MethodGet, url, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: %d %v", url, resp.StatusCode, out.Errors)
	}
	return revisionOf(t, resp)
}

// putRevision writes a chamber whose flag "beta" is value at path, and
// returns the revision its inherited view then has.
func putRevision(t *testing.T, base, path, value string) uint64 {
	t.Helper()
	putChamber(t, base, path, fmt.Sprintf(flagBody, value))
	return getRevision(t, base+"/v1/chambers/"+path)
}

// TestChamberReadsCarryRevisions verifies that chamber reads and evaluations
// report the revision of the inherited view: the highest among the chamber
// and its parents, so a parent's write changes it.
func TestChamberReadsCarryRevisions(t *testing.T) {
	base := newRevisionTestServer(t)
	chamber := base + "/v1/chambers/"

	putChamber(t, base, "a/", fmt.Sprintf(flagBody, "true"))
	inherited := putRevision(t, base, "", "false")
	own := getRevision(t, chamber+"a/?inherit=false")
	if got := getRevision(t, chamber+"a/"); got != inherited || got <= own {
		t.Errorf("inherited revision %d, want the root's %d, above the chamber's own %d", got, inherited, own)
	}

	after := putRevision(t, base, "", "true")
	if got := getRevision(t, chamber+"a/"); got != after || after <= inherited {
		t.Errorf("after a parent write the revision is %d, want %d, above %d", got, after, inherited)
	}
	if got := getRevision(t, chamber+"a/?inherit=false"); got != own {
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
	putChamber(t, srv.URL, "a/", fmt.Sprintf(flagBody, "true"))
	resp, _ := do(t, http.MethodGet, srv.URL+"/v1/chambers/a/", "", nil)
	if h, ok := resp.Header[api.RevisionHeader]; ok {
		t.Errorf("%s = %q, want none", api.RevisionHeader, h)
	}
}

// TestStreamEventsCarryRevisions verifies that each event of a chamber stream
// and an evaluation stream has the revision it was read at as its id.
func TestStreamEventsCarryRevisions(t *testing.T) {
	base := newRevisionTestServer(t)
	first := putRevision(t, base, "a/", "true")

	streams := map[string]func() (*http.Response, error){
		"chamber": func() (*http.Response, error) {
			return http.Get(base + "/v1/chambers/a/?watch=true")
		},
		"evaluation": func() (*http.Response, error) {
			return http.Post(base+"/v1/evaluate/a/?watch=true", "application/json", strings.NewReader(`{"key":"u"}`))
		},
	}
	ids := map[string]<-chan string{}
	for name, open := range streams {
		resp, err := open()
		if err != nil {
			t.Fatalf("%s stream: %v", name, err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		ids[name] = fieldLines(bufio.NewScanner(resp.Body), "id")
	}
	for name := range streams {
		if got := waitData(t, ids[name], 5*time.Second); got != strconv.FormatUint(first, 10) {
			t.Errorf("%s: first event id %q, want %d", name, got, first)
		}
	}

	second := putRevision(t, base, "a/", "false")
	for name := range streams {
		if got := waitData(t, ids[name], 5*time.Second); got != strconv.FormatUint(second, 10) {
			t.Errorf("%s: event id after a write %q, want %d", name, got, second)
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
			first := putRevision(t, base, "a/", "true")

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

			second := putRevision(t, base, "a/", "false")
			deadline := time.Now().Add(5 * time.Second)
			for rlm.Revision(ctx) != second && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := rlm.Revision(ctx); got != second {
				t.Errorf("revision after a write = %d, want %d", got, second)
			}
			if got := rlm.Revision(pinned); got != first {
				t.Errorf("a pinned context's revision is %d, want %d", got, first)
			}
			if v, err := rlm.Bool(pinned, "beta", false); err != nil || !v {
				t.Errorf("pinned beta = %v, %v; want true", v, err)
			}
		})
	}
}
