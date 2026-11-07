package http

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatalf("could not create storage: %v", err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg, RequestTimeout: DefaultHandlerTimeout})
	if err != nil {
		t.Fatalf("could not create handler: %v", err)
	}
	return h
}

func putChamber(t *testing.T, baseURL, path, body string) {
	t.Helper()
	resp, err := http.Post(baseURL+"/v1/chambers/"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("put %q: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put %q: expected 201, got %d", path, resp.StatusCode)
	}
}

// dataEvents reads an SSE body and emits the payload of each `data:` line.
func dataEvents(scanner *bufio.Scanner) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				out <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return out
}

func waitData(t *testing.T, events <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case d, ok := <-events:
		if !ok {
			t.Fatal("event stream closed unexpectedly")
		}
		return d
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an SSE event")
		return ""
	}
}

func TestStreamChamberPushesInitialAndUpdates(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t))
	defer srv.Close()

	putChamber(t, srv.URL, "root", `{"rules":{"flag":{"type":"string","value":"one"}}}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/chambers/root?watch=true", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	events := dataEvents(bufio.NewScanner(resp.Body))

	if first := waitData(t, events, 2*time.Second); !strings.Contains(first, `"one"`) {
		t.Fatalf("initial event missing seeded value: %s", first)
	}

	// A subsequent write must be pushed to the open stream.
	putChamber(t, srv.URL, "root", `{"rules":{"flag":{"type":"string","value":"two"}}}`)

	if second := waitData(t, events, 2*time.Second); !strings.Contains(second, `"two"`) {
		t.Fatalf("update event missing new value: %s", second)
	}
}

// TestStreamChamberFramesMultiLinePayloads checks that a chamber stored
// pretty-printed (edited by hand, or written by a tool that formats JSON)
// reaches clients whole. Every line of an event's payload must be a data
// field: a client takes any other line for a field it does not know and
// drops it, and is left with a fragment of the chamber.
func TestStreamChamberFramesMultiLinePayloads(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	pretty := "{\n  \"rules\": {\n    \"flag\": {\n      \"type\": \"string\",\n      \"value\": \"one\"\n    }\n  }\n}\n"
	if err := stg.Put(context.Background(), storage.StorageEntry{Key: "/root/", Value: []byte(pretty)}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/v1/chambers/root?watch=true")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	stop := time.AfterFunc(5*time.Second, func() { resp.Body.Close() })
	defer stop.Stop()

	// read the first event as a client does
	var data, stray []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "" && len(data) > 0:
		case line == "", strings.HasPrefix(line, "event:"), strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		default:
			stray = append(stray, line)
			continue
		}
		break
	}
	if len(stray) > 0 {
		t.Errorf("lines of the event that are not fields: %q", stray)
	}
	var chamber struct {
		Rules map[string]struct{ Value string }
	}
	if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &chamber); err != nil || chamber.Rules["flag"].Value != "one" {
		t.Errorf("event data = %q, which does not hold the chamber (%v)", data, err)
	}
}
