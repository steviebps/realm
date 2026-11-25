package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/auth"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// newBrokenParentReader returns a chamberReader over inheritable storage in
// which /prod/ cannot be parsed and /prod/eu/ below it can.
func newBrokenParentReader(t *testing.T) *chamberReader {
	t.Helper()
	ctx := context.Background()
	raw := newBigCache(t)
	for k, v := range map[string]string{
		"/prod/":    `{"rules":{"x":{"type":"boolean","value":"no"}}}`,
		"/prod/eu/": `{"rules":{"y":{"type":"boolean","value":true}}}`,
	} {
		if err := raw.Put(ctx, storage.StorageEntry{Key: k, Value: []byte(v)}); err != nil {
			t.Fatal(err)
		}
	}
	inh, err := storage.NewInheritableStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	return newChamberReader(inh, newBroker(0), time.Second)
}

// TestReadFailuresTellOnlyThoseWhoMayRead checks that why a chamber cannot be
// parsed is told only to principals that may read it, on every read route
// the chamber reader serves.
func TestReadFailuresTellOnlyThoseWhoMayRead(t *testing.T) {
	ctx := context.Background()
	reader := newBrokenParentReader(t)
	scoped := &auth.Principal{Kind: auth.KindKey, ID: "k", Access: auth.AccessRead, Path: "/prod/eu/"}
	evaluateKey := &auth.Principal{Kind: auth.KindKey, ID: "e", Access: auth.AccessEvaluate, Path: "/"}
	principals := []struct {
		name string
		p    *auth.Principal
		told bool
	}{
		{"may read the broken parent", auth.Anonymous, true},
		{"may read only below it", scoped, false},
		{"an evaluate key", evaluateKey, false},
	}

	_, readErr := reader.Chamber(ctx, "/prod/eu/", false)
	_, evalErr := reader.Evaluate(ctx, "/prod/eu/", realm.EvaluationContext{}, false)
	_, watchErr := reader.Watched(ctx, "/prod/eu/")
	view, err := reader.views.view(ctx, "/prod/eu/")
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"read": readErr, "evaluation": evalErr, "watch": watchErr} {
		var ce *storage.ChamberError
		if !errors.As(err, &ce) || ce.Path != "/prod/" {
			t.Fatalf("%s: %v, want the broken parent's ChamberError", name, err)
		}
	}

	for _, pr := range principals {
		t.Run(pr.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			failRead(ctx, rec, pr.p, readErr)
			body := rec.Body.String()
			if rec.Code != http.StatusInternalServerError || strings.Contains(body, "/prod/") != pr.told {
				t.Errorf("response %d %s; want 500, told why: %v", rec.Code, body, pr.told)
			}

			data, _ := view.event(pr.p)
			if strings.Contains(string(data), "could not parse parent chamber") != pr.told || !strings.Contains(string(data), "/prod/eu/") {
				t.Errorf("subtree event %s; want its path, told why: %v", data, pr.told)
			}
			if got := visibleError(pr.p, readErr); (got != hiddenChamberError) != pr.told {
				t.Errorf("visibleError = %q, told why: %v", got, pr.told)
			}
		})
	}
}

// TestReadStatus checks the status each read failure maps to.
func TestReadStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"missing", &storage.NotFoundError{Key: "/a/"}, http.StatusNotFound},
		{"invalid path", storage.ErrInvalidPath, http.StatusBadRequest},
		{"broken chamber", &storage.ChamberError{Path: "/a/", Err: errors.New("bad")}, http.StatusInternalServerError},
		{"outage", errors.New("unreachable"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		if got, _ := readStatus(auth.Anonymous, tt.err); got != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, got, tt.want)
		}
	}
}

// TestEvaluateMissingChamber checks that both kinds of evaluation report a
// missing chamber as not found, so streams wait for it.
func TestEvaluateMissingChamber(t *testing.T) {
	reader := newBrokenParentReader(t)
	for _, shared := range []bool{false, true} {
		var nf *storage.NotFoundError
		if _, err := reader.Evaluate(context.Background(), "/nope/", realm.EvaluationContext{}, shared); !errors.As(err, &nf) {
			t.Errorf("shared %v: %v, want a NotFoundError", shared, err)
		}
	}
}
