package http

import (
	"bufio"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/internal/evaluations/evaluationstest"
)

type series = evaluationstest.Series

// TestEvaluateCountsEachFlagServed checks that the evaluation endpoint
// counts each flag it serves, inherited ones included, at the path asked
// for, and that a failed evaluation counts nothing.
func TestEvaluateCountsEachFlagServed(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "counted/", evaluateParent)
	putChamber(t, srv.URL, "counted/web/", `{"rules":{"beta":{"type":"boolean","value":false,"targeting":[
		{"id":"pilots","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}`)

	for _, tenant := range []string{"acme", "acme", "initech"} {
		body := `{"attributes":{"tenantId":"` + tenant + `"}}`
		if status, out := call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/counted/web/", body, ""); status != http.StatusOK {
			t.Fatalf("status = %d (%v)", status, out.Errors)
		}
	}
	if status, _ := call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/counted/missing/", `{}`, ""); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}

	path := "/counted/web/"
	want := map[series]int64{
		{Flag: "beta", Path: path, Rule: "pilots"}:   2,
		{Flag: "beta", Path: path, Rule: "default"}:  1,
		{Flag: "theme", Path: path, Rule: "default"}: 3,
	}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
	if got := evaluationstest.Collect(t, "/counted/missing/"); len(got) != 0 {
		t.Errorf("a missing chamber counted %v", got)
	}
}

// TestEvaluateStreamCountsChangesOfRule checks that an evaluation stream
// counts each flag of its first event, then a flag again only when an event
// it sends serves it by another rule: not for a change that sends nothing,
// nor for one that changes only the flag's value, or another flag.
func TestEvaluateStreamCountsChangesOfRule(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "streamed/", `{"rules":{"theme":{"type":"string","value":"light"},"banner":{"type":"string","value":"hi"}}}`)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evaluate/streamed/?watch=true", strings.NewReader(`{"attributes":{"tenantId":"acme"}}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := dataEvents(bufio.NewScanner(resp.Body))
	waitData(t, events, 2*time.Second)

	// each write is followed by the event it sends, except the first, which
	// does not change acme's values
	writes := []struct{ theme, want string }{
		{`{"id":"others","clauses":[{"attribute":"tenantId","op":"in","values":["other"]}],"value":"dark"}`, ""},
		{`{"id":"dark-acme","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":"dark"}`, `"dark"`},
		{`{"id":"dark-acme","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":"black"}`, `"black"`},
	}
	for _, w := range writes {
		putChamber(t, srv.URL, "streamed/", `{"rules":{"theme":{"type":"string","value":"light","targeting":[`+w.theme+`]},"banner":{"type":"string","value":"hi"}}}`)
		if w.want == "" {
			continue
		}
		if next := waitData(t, events, 2*time.Second); !strings.Contains(next, w.want) {
			t.Fatalf("next event = %s, want %s", next, w.want)
		}
	}

	// the last event counted nothing, so the counts of those before it are in
	path := "/streamed/"
	want := map[series]int64{
		{Flag: "theme", Path: path, Rule: "default"}:   1,
		{Flag: "theme", Path: path, Rule: "dark-acme"}: 1,
		{Flag: "banner", Path: path, Rule: "default"}:  1,
	}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
}
