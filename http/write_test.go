package http

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// newTestWriter returns a chamberWriter over fresh inheritable storage, the
// raw storage beneath it, and the paths it announced so far.
func newTestWriter(t *testing.T) (*chamberWriter, storage.Storage, *[]string) {
	t.Helper()
	raw := newBigCache(t)
	inh, err := storage.NewInheritableStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	var notified []string
	return newChamberWriter(inh, func(p string) { notified = append(notified, p) }), raw, &notified
}

func mustChamber(t *testing.T, s string) *realm.Chamber {
	t.Helper()
	c, err := realm.ParseChamber([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestChamberWriter drives the chamber write module through its interface,
// without HTTP: each step's refusal, and what it stores, records, and
// announces.
func TestChamberWriter(t *testing.T) {
	ctx := context.Background()
	w, raw, notified := newTestWriter(t)

	put := func(p, body string) writeRequest {
		return writeRequest{Path: p, Operation: PutOperation, Body: mustChamber(t, body), Actor: "tester"}
	}
	var (
		sealed       *sealedError
		constrained  *constraintError
		precondition *preconditionError
		broken       *storage.ChamberError
		notFound     *storage.NotFoundError
	)
	steps := []struct {
		name string
		req  writeRequest
		// want reports whether err is the refusal expected; nil wants success
		want func(error) bool
	}{
		{"the root seals a flag", put("/", `{"rules":{"kill":{"type":"boolean","value":false,"sealed":true},"hold":{"type":"number","value":24,"constraints":{"maximum":72}}}}`), nil},
		{"a child cannot define it", put("/a/", `{"rules":{"kill":{"type":"boolean","value":true}}}`), func(err error) bool { return errors.As(err, &sealed) }},
		{"a child breaks a constraint", put("/a/", `{"rules":{"hold":{"type":"number","value":100}}}`), func(err error) bool { return errors.As(err, &constrained) }},
		{"a child within it", put("/a/", `{"rules":{"hold":{"type":"number","value":48}}}`), nil},
		{"a stale precondition", writeRequest{Path: "/a/", Operation: PutOperation, Body: mustChamber(t, `{"rules":{}}`), Preconditions: preconditions{IfMatch: `"stale"`}}, func(err error) bool { return errors.As(err, &precondition) && precondition.current != "" }},
		{"a patch of nothing", writeRequest{Path: "/none/", Operation: PatchOperation, Body: mustChamber(t, `{"rules":{}}`)}, func(err error) bool { return errors.Is(err, errPatchMissing) }},
		{"deleting everything", writeRequest{Path: "/", Operation: DeleteOperation, Recursive: true}, func(err error) bool { return errors.Is(err, errDeleteEverything) }},
	}
	for _, s := range steps {
		*notified = nil
		_, err := w.Write(ctx, s.req)
		switch {
		case s.want == nil && err != nil:
			t.Fatalf("%s: %v", s.name, err)
		case s.want != nil && !s.want(err):
			t.Fatalf("%s: err = %v, not the refusal wanted", s.name, err)
		}
		wantNotified := []string(nil)
		if s.want == nil {
			wantNotified = []string{s.req.Path}
		}
		if !slices.Equal(*notified, wantNotified) {
			t.Errorf("%s: announced %v, want %v", s.name, *notified, wantNotified)
		}
	}

	// the accepted writes are in the history, with their actor
	meta := &metaStore{strg: raw}
	revs, err := meta.history(ctx, "/a/")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].Actor != "tester" || revs[0].Operation != PutOperation {
		t.Errorf("history of /a/ = %+v, want the one accepted PUT by tester", revs)
	}

	// a chamber below a broken parent is refused, as reads below it fail
	if err := raw.Put(ctx, storage.StorageEntry{Key: "/b/", Value: []byte(`{"rules":{"x":{"type":"boolean","value":"no"}}}`)}); err != nil {
		t.Fatal(err)
	}
	*notified = nil
	if _, err := w.Write(ctx, put("/b/c/", `{"rules":{}}`)); !errors.As(err, &broken) || broken.Path != "/b/" {
		t.Fatalf("below a broken parent: %v, want its ChamberError", err)
	}
	if len(*notified) != 0 {
		t.Errorf("a refused write announced %v", *notified)
	}
	if _, err := raw.Get(ctx, "/b/c/"); !errors.As(err, &notFound) {
		t.Errorf("the refused chamber was stored: %v", err)
	}
	// the broken chamber itself may be replaced
	if _, err := w.Write(ctx, put("/b/", `{"rules":{}}`)); err != nil {
		t.Fatalf("replacing the broken chamber: %v", err)
	}

	// a recursive delete removes and announces every chamber below
	if _, err := w.Write(ctx, put("/a/x/", `{"rules":{}}`)); err != nil {
		t.Fatal(err)
	}
	*notified = nil
	res, err := w.Write(ctx, writeRequest{Path: "/a/", Operation: DeleteOperation, Recursive: true, Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/a/", "/a/x/"}; !slices.Equal(res.Deleted, want) || !slices.Equal(*notified, want) {
		t.Errorf("deleted %v, announced %v; want %v both", res.Deleted, *notified, want)
	}
}

// TestCheckBelowHonorsSeals checks that a constraint added at a chamber is
// not held against definitions below that a seal between them hides, nor
// against those the new chamber itself seals.
func TestCheckBelowHonorsSeals(t *testing.T) {
	ctx := context.Background()
	w, raw, _ := newTestWriter(t)
	for _, e := range []storage.StorageEntry{
		{Key: "/t/a/", Value: []byte(`{"rules":{"hold":{"type":"number","value":10,"sealed":true}}}`)},
		{Key: "/t/a/b/", Value: []byte(`{"rules":{"hold":{"type":"number","value":500}}}`)},
		{Key: "/t/c/", Value: []byte(`{"rules":{"hold":{"type":"number","value":500}}}`)},
	} {
		if err := raw.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	constrain := func(sealed bool) error {
		body := `{"rules":{"hold":{"type":"number","value":10,"constraints":{"maximum":72}}}}`
		if sealed {
			body = `{"rules":{"hold":{"type":"number","value":10,"sealed":true,"constraints":{"maximum":72}}}}`
		}
		_, err := w.Write(ctx, writeRequest{Path: "/t/", Operation: PutOperation, Body: mustChamber(t, body)})
		return err
	}

	// /t/a/b/ is hidden by /t/a/'s seal; /t/c/ is not
	var ce *constraintError
	if err := constrain(false); !errors.As(err, &ce) || len(ce.reasons) != 1 || !strings.Contains(ce.reasons[0], "/t/c/") || strings.Contains(ce.reasons[0], "/t/a/b/") {
		t.Fatalf("constraining /t/: %v, want only /t/c/ named", err)
	}
	// sealing it at /t/ hides every definition below
	if err := constrain(true); err != nil {
		t.Fatalf("sealing and constraining /t/: %v", err)
	}
}

// TestWriteBelowBrokenParentStatus checks that the server refuses a write
// below a chamber that cannot be parsed with 409, naming that chamber.
func TestWriteBelowBrokenParentStatus(t *testing.T) {
	stg := newBigCache(t)
	if err := stg.Put(context.Background(), storage.StorageEntry{Key: "/b/", Value: []byte(`{"rules":{"x":{"type":"boolean","value":"no"}}}`)}); err != nil {
		t.Fatal(err)
	}
	srv := newInheritableTestServerOn(t, stg)
	status, out := call(t, nil, "POST", srv.URL+"/v1/chambers/b/c/", `{"rules":{}}`, "")
	if status != 409 || len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "could not parse parent chamber /b/") {
		t.Fatalf("PUT below a broken parent = %d %v, want 409 naming /b/", status, out.Errors)
	}
}
