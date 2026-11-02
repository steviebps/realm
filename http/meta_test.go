package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/pkg/storage"
)

func newInheritableTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	inh, err := storage.NewInheritableStorage(stg)
	if err != nil {
		t.Fatalf("inheritable: %v", err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: inh})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string, headers map[string]string) (*http.Response, api.HTTPErrorAndDataResponse) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out api.HTTPErrorAndDataResponse
	b, _ := io.ReadAll(resp.Body)
	if len(b) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return resp, out
}

const flagBody = `{"rules":{"beta":{"type":"boolean","value":%s}}}`

func TestChamberOptimisticConcurrency(t *testing.T) {
	srv := newInheritableTestServer(t)
	url := srv.URL + "/v1/chambers/app/"

	// If-None-Match: * creates only when absent.
	resp, _ := do(t, http.MethodPost, url, strings.Replace(flagBody, "%s", "false", 1), map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}
	created := resp.Header.Get("ETag")
	if created == "" {
		t.Fatal("expected an ETag on create")
	}
	resp, _ = do(t, http.MethodPost, url, strings.Replace(flagBody, "%s", "false", 1), map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("second create-only: expected 412, got %d", resp.StatusCode)
	}

	resp, _ = do(t, http.MethodGet, url+"?inherit=false", "", nil)
	if got := resp.Header.Get("ETag"); got != created {
		t.Fatalf("GET ETag %q does not match create ETag %q", got, created)
	}
	resp, _ = do(t, http.MethodGet, url, "", nil)
	if got := resp.Header.Get("ETag"); got != "" {
		t.Fatalf("inherited GET should not carry the stored chamber's ETag, got %q", got)
	}

	// A write with the current ETag succeeds and changes the ETag.
	resp, _ = do(t, http.MethodPost, url, strings.Replace(flagBody, "%s", "true", 1), map[string]string{"If-Match": created})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("matching write: expected 201, got %d", resp.StatusCode)
	}
	updated := resp.Header.Get("ETag")
	if updated == "" || updated == created {
		t.Fatalf("expected a new ETag, got %q", updated)
	}

	// A write with the stale ETag is rejected, reporting the current one.
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		resp, _ = do(t, method, url, strings.Replace(flagBody, "%s", "false", 1), map[string]string{"If-Match": created})
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("%s with stale ETag: expected 412, got %d", method, resp.StatusCode)
		}
		if resp.Header.Get("ETag") != updated {
			t.Fatalf("%s 412 should report current ETag %q, got %q", method, updated, resp.Header.Get("ETag"))
		}
	}
}

func TestParentWriteDoesNotChangeChildETag(t *testing.T) {
	srv := newInheritableTestServer(t)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", strings.Replace(flagBody, "%s", "false", 1), nil)
	resp, _ := do(t, http.MethodPost, srv.URL+"/v1/chambers/app/prod/", `{"rules":{}}`, nil)
	child := resp.Header.Get("ETag")

	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", strings.Replace(flagBody, "%s", "true", 1), nil)

	resp, _ = do(t, http.MethodGet, srv.URL+"/v1/chambers/app/prod/?inherit=false", "", nil)
	if resp.Header.Get("ETag") != child {
		t.Error("editing a parent should not invalidate the child's ETag")
	}
}

func TestPatchDoesNotCopyInheritedRules(t *testing.T) {
	srv := newInheritableTestServer(t)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", strings.Replace(flagBody, "%s", "false", 1), nil)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/prod/", `{"rules":{}}`, nil)
	do(t, http.MethodPatch, srv.URL+"/v1/chambers/app/prod/", `{"rules":{"other":{"type":"string","value":"x"}}}`, nil)

	// Change the parent: the child must still inherit it.
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", strings.Replace(flagBody, "%s", "true", 1), nil)

	_, out := do(t, http.MethodGet, srv.URL+"/v1/chambers/app/prod/", "", nil)
	var c struct {
		Rules map[string]struct{ Value any } `json:"rules"`
	}
	if err := json.Unmarshal(out.Data, &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.Rules["beta"].Value != true {
		t.Errorf("child should inherit the parent's updated value, got %v (patch copied the inherited rule)", c.Rules["beta"].Value)
	}
}

func TestGetWithoutInheritance(t *testing.T) {
	srv := newInheritableTestServer(t)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", strings.Replace(flagBody, "%s", "false", 1), nil)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/prod/", `{"rules":{"own":{"type":"string","value":"x"}}}`, nil)

	decode := func(raw json.RawMessage) map[string]any {
		var c struct{ Rules map[string]any }
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return c.Rules
	}
	_, merged := do(t, http.MethodGet, srv.URL+"/v1/chambers/app/prod/", "", nil)
	if rules := decode(merged.Data); len(rules) != 2 {
		t.Errorf("inherited view should have 2 rules, got %v", rules)
	}
	_, own := do(t, http.MethodGet, srv.URL+"/v1/chambers/app/prod/?inherit=false", "", nil)
	if rules := decode(own.Data); len(rules) != 1 || rules["own"] == nil {
		t.Errorf("inherit=false should return only the chamber's own rules, got %v", rules)
	}
}

func TestWriteReportsValidationErrors(t *testing.T) {
	srv := newInheritableTestServer(t)
	resp, out := do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", `{"rules":{"f":{"type":"boolean","value":false,"overrides":[]}}}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "no longer supported") {
		t.Errorf("expected migration guidance in error, got %v", out.Errors)
	}
}

func TestHistoryRecordsChanges(t *testing.T) {
	srv := newInheritableTestServer(t)
	url := srv.URL + "/v1/chambers/app/"
	do(t, http.MethodPost, url, strings.Replace(flagBody, "%s", "false", 1), map[string]string{ActorHeader: "alice@example.com"})
	do(t, http.MethodPost, url, strings.Replace(flagBody, "%s", "true", 1), map[string]string{ActorHeader: "bob@example.com"})
	do(t, http.MethodDelete, url, "", nil)

	resp, out := do(t, http.MethodGet, srv.URL+"/v1/history/app/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("history: expected 200, got %d %v", resp.StatusCode, out.Errors)
	}
	var revs []Revision
	if err := json.Unmarshal(out.Data, &revs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("expected 3 revisions, got %d", len(revs))
	}
	want := []struct {
		rev   int
		actor string
		op    Operation
	}{{3, defaultActor, DeleteOperation}, {2, "bob@example.com", PutOperation}, {1, "alice@example.com", PutOperation}}
	for i, w := range want {
		if revs[i].Revision != w.rev || revs[i].Actor != w.actor || revs[i].Operation != w.op {
			t.Errorf("revision[%d] = %+v, want %+v", i, revs[i], w)
		}
		if revs[i].Chamber != nil {
			t.Errorf("history listing should omit chamber bodies")
		}
	}

	_, out = do(t, http.MethodGet, srv.URL+"/v1/history/app/?revision=1", "", nil)
	var one Revision
	if err := json.Unmarshal(out.Data, &one); err != nil {
		t.Fatalf("decode revision: %v", err)
	}
	var c struct {
		Rules map[string]struct{ Value any } `json:"rules"`
	}
	if err := json.Unmarshal(one.Chamber, &c); err != nil || c.Rules["beta"].Value != false {
		t.Errorf("revision 1 should hold the original chamber, got %s (%v)", one.Chamber, err)
	}

	resp, _ = do(t, http.MethodGet, srv.URL+"/v1/history/app/?revision=99", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing revision: expected 404, got %d", resp.StatusCode)
	}
}

func TestReservedPathsAreHidden(t *testing.T) {
	srv := newInheritableTestServer(t)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/", `{"rules":{}}`, nil)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/app/", `{"rules":{}}`, nil)
	do(t, http.MethodPost, srv.URL+"/v1/chambers/_realmx/", `{"rules":{}}`, nil)
	do(t, http.MethodPatch, srv.URL+"/v1/catalog/locationId", `{"values":{"a":"A"}}`, nil)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		resp, _ := do(t, method, srv.URL+"/v1/chambers/_realm/catalog/", `{"rules":{}}`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s on reserved path: expected 400, got %d", method, resp.StatusCode)
		}
	}

	_, out := do(t, http.MethodGet, srv.URL+"/v1/chambers/?list=true", "", nil)
	var names []string
	if err := json.Unmarshal(out.Data, &names); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range names {
		if isReservedName(name) {
			t.Errorf("root listing exposes metadata: %v", names)
		}
	}
	if !strings.Contains(string(out.Data), "_realmx") {
		t.Errorf("a chamber merely prefixed with _realm should still be listed: %v", names)
	}
}

func TestCatalog(t *testing.T) {
	srv := newInheritableTestServer(t)
	base := srv.URL + "/v1/catalog/"

	resp, _ := do(t, http.MethodGet, base+"locationId", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown attribute: expected 404, got %d", resp.StatusCode)
	}

	do(t, http.MethodPost, base+"locationId", `{"values":{"loc-1":"Downtown","loc-2":"Airport"}}`, nil)
	_, out := do(t, http.MethodPatch, base+"locationId", `{"values":{"loc-2":null,"loc-3":"Harbor"}}`, nil)
	var got struct{ Values map[string]string }
	if err := json.Unmarshal(out.Data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]string{"loc-1": "Downtown", "loc-3": "Harbor"}
	if len(got.Values) != len(want) || got.Values["loc-1"] != "Downtown" || got.Values["loc-3"] != "Harbor" {
		t.Errorf("after patch values = %v, want %v", got.Values, want)
	}

	do(t, http.MethodPost, base+"environment", `{"values":{"prod":"Production"}}`, nil)
	_, out = do(t, http.MethodGet, srv.URL+"/v1/catalog", "", nil)
	var all Catalog
	if err := json.Unmarshal(out.Data, &all); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(all) != 2 || all["environment"]["prod"] != "Production" {
		t.Errorf("catalog = %v", all)
	}

	resp, _ = do(t, http.MethodPost, base+"locationId", `{"values":{"x":null}}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("null label on replace: expected 400, got %d", resp.StatusCode)
	}

	resp, _ = do(t, http.MethodDelete, base+"environment", "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodDelete, base+"environment", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: expected 404, got %d", resp.StatusCode)
	}
}
