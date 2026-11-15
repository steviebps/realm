package http

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
)

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"gzip"}, true},
		{[]string{"GZIP"}, true},
		{[]string{"x-gzip"}, true},
		{[]string{"gzip, deflate, br"}, true},
		{[]string{"br;q=1.0, gzip;q=0.5"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip; q=0.000"}, false},
		{[]string{"gzip;q=0.001"}, true},
		{[]string{"gzip;q=nope"}, false},
		{[]string{"gzip;q=2"}, false},
		{[]string{"identity"}, false},
		{[]string{"br, deflate"}, false},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"*, gzip;q=0"}, false},
		{[]string{"gzip;q=0, *"}, false},
		{[]string{"identity", "gzip"}, true},
		{[]string{" gzip ; level=1 "}, true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, v := range tt.header {
			r.Header.Add("Accept-Encoding", v)
		}
		if got := acceptsGzip(r); got != tt.want {
			t.Errorf("Accept-Encoding %q: acceptsGzip = %v, want %v", tt.header, got, tt.want)
		}
	}
}

// rawClient returns a client that neither asks for compressed responses
// nor decodes them, so tests see bodies as sent.
func rawClient(t testing.TB) *http.Client {
	t.Helper()
	tr := &http.Transport{DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// getEncoded sends a GET for url with Accept-Encoding acceptEncoding
// (none when empty), returning the response and its body decoded as its
// Content-Encoding says. The caller closes the response's body.
func getEncoded(t testing.TB, client *http.Client, url, acceptEncoding string) (*http.Response, io.Reader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	switch ce := resp.Header.Get("Content-Encoding"); ce {
	case "":
		return resp, resp.Body
	case "gzip":
		// reads the gzip header, which the server sends with its first
		// flush
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return resp, zr
	default:
		t.Fatalf("GET %s: Content-Encoding %q", url, ce)
		return nil, nil
	}
}

// varies reports whether h says the response varies by Accept-Encoding.
func varies(h http.Header) bool {
	for _, v := range h.Values("Vary") {
		for f := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

// seedWide writes subtreeTree and n more tenants through the API at base,
// so that a subtree read of /prod/ spans several flushes.
func seedWide(t *testing.T, base string, n int) {
	t.Helper()
	seedSubtree(t, base)
	for i := range n {
		putChamber(t, base, fmt.Sprintf("prod/tenants/w-%03d", i), fmt.Sprintf(`{"rules":{"limit":{"type":"number","value":%d}}}`, i))
	}
}

// TestSubtreeReadCompression verifies a subtree read is gzipped exactly
// when the request accepts gzip and the server allows it, and that the
// gzipped body decodes to the identity body.
func TestSubtreeReadCompression(t *testing.T) {
	compressed := subtreeTestServer(t, newBigCache(t), nil)
	disabled := subtreeTestServer(t, newBigCache(t), func(c *HandlerConfig) { c.DisableSubtreeCompression = true })
	for _, base := range []string{compressed, disabled} {
		// more entries than one flush holds (subtreeFlushEvery)
		seedWide(t, base, 3*subtreeFlushEvery)
	}
	client := rawClient(t)
	read := func(base, acceptEncoding string) (*http.Response, string) {
		t.Helper()
		resp, body := getEncoded(t, client, base+"/v1/chambers/prod/?subtree=true", acceptEncoding)
		b, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read with Accept-Encoding %q: %v", acceptEncoding, err)
		}
		return resp, string(b)
	}
	_, identity := read(compressed, "")
	var out struct {
		Data struct {
			Chambers []subtreeView `json:"chambers"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(identity), &out); err != nil || len(out.Data.Chambers) != 5+3*subtreeFlushEvery {
		t.Fatalf("identity read: %d entries, %v", len(out.Data.Chambers), err)
	}

	tests := []struct {
		name, base, acceptEncoding string
		gzipped                    bool
	}{
		{"no Accept-Encoding", compressed, "", false},
		{"gzip", compressed, "gzip", true},
		{"a browser's", compressed, "gzip, deflate, br, zstd", true},
		{"gzip refused", compressed, "gzip;q=0", false},
		{"identity", compressed, "identity", false},
		{"other codings", compressed, "br", false},
		{"any coding", compressed, "*", true},
		{"compression disabled", disabled, "gzip", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := read(tt.base, tt.acceptEncoding)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Encoding") == "gzip"; got != tt.gzipped {
				t.Errorf("gzipped = %v, want %v", got, tt.gzipped)
			}
			if !varies(resp.Header) {
				t.Errorf("Vary = %q, want Accept-Encoding", resp.Header.Values("Vary"))
			}
			if body != identity {
				t.Errorf("decoded body differs from the identity body:\n%.300s\nwant\n%.300s", body, identity)
			}
		})
	}
}

// heldStorage holds reads of one key, once armed, until release is closed.
type heldStorage struct {
	storage.Storage
	key     string
	armed   atomic.Bool
	release chan struct{}
}

func (h *heldStorage) Get(ctx context.Context, key string) (*storage.StorageEntry, error) {
	if key == h.key && h.armed.Load() {
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.Storage.Get(ctx, key)
}

// TestSubtreeReadIsFlushedThroughGzip verifies that a gzipped subtree read
// sends its entries as it reads them: the client decodes the first flush's
// entries while the server is still reading.
func TestSubtreeReadIsFlushedThroughGzip(t *testing.T) {
	held := &heldStorage{Storage: newBigCache(t), key: "/prod/tenants/w-150/", release: make(chan struct{})}
	base := subtreeTestServer(t, held, nil)
	seedWide(t, base, 3*subtreeFlushEvery)
	held.armed.Store(true)
	released := false
	defer func() {
		if !released {
			close(held.release)
		}
	}()

	resp, body := getEncoded(t, rawClient(t), base+"/v1/chambers/prod/?subtree=true", "gzip")
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding %q", ce)
	}
	dec := json.NewDecoder(body)
	for _, want := range []string{"{", "data", "{", "chambers", "["} {
		tok, err := dec.Token()
		if err != nil || fmt.Sprint(tok) != want {
			t.Fatalf("token %v, %v; want %s", tok, err, want)
		}
	}
	entries := make(chan subtreeView)
	go func() {
		defer close(entries)
		for dec.More() {
			var v subtreeView
			if err := dec.Decode(&v); err != nil {
				return
			}
			entries <- v
		}
	}()
	for i := range subtreeFlushEvery {
		select {
		case _, ok := <-entries:
			if !ok {
				t.Fatalf("the body ended after %d entries", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d entries arrived while the read was held, want the first flush's %d", i, subtreeFlushEvery)
		}
	}
	close(held.release)
	released = true
	n := subtreeFlushEvery
	for range entries {
		n++
	}
	if want := 5 + 3*subtreeFlushEvery; n != want {
		t.Errorf("%d entries, want %d", n, want)
	}
}

// TestSubtreeErrorsAreNotCompressed verifies that a subtree read or watch
// refused before its body starts is answered uncompressed. A refusal of a
// valid subtree request varies by Accept-Encoding like the response it
// stands for; a refused combination is no subtree request.
func TestSubtreeErrorsAreNotCompressed(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), func(c *HandlerConfig) {
		c.MaxSubtreeChambers = 3
		c.MaxStreams = 1
	})
	seedSubtree(t, base)
	client := rawClient(t)

	// in order: the last takes the one stream the server allows first
	tests := []struct {
		name, url string
		status    int
		vary      bool
		// hold opens a stream first, and holds it
		hold bool
	}{
		{"a refused combination", "/v1/chambers/prod/?subtree=true&inherit=false", http.StatusBadRequest, false, false},
		{"a refused watch combination", "/v1/chambers/prod/?subtree=true&inherit=layers&watch=true", http.StatusBadRequest, false, false},
		{"too large", "/v1/chambers/prod/?subtree=true", http.StatusRequestEntityTooLarge, true, false},
		{"too large to watch", "/v1/chambers/prod/?subtree=true&watch=true", http.StatusRequestEntityTooLarge, true, false},
		{"too many streams", "/v1/chambers/prod/tenants/t-2/?subtree=true&watch=true", http.StatusServiceUnavailable, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hold {
				if resp, _ := getEncoded(t, client, base+"/v1/chambers/prod/tenants/t-1/?subtree=true&watch=true", "gzip"); resp.StatusCode != http.StatusOK {
					t.Fatalf("first stream: %d", resp.StatusCode)
				}
			}
			resp, body := getEncoded(t, client, base+tt.url, "gzip")
			if resp.StatusCode != tt.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tt.status)
			}
			if ce := resp.Header.Get("Content-Encoding"); ce != "" {
				t.Errorf("Content-Encoding %q on an error", ce)
			}
			if varies(resp.Header) != tt.vary {
				t.Errorf("Vary = %q; want Accept-Encoding: %v", resp.Header.Values("Vary"), tt.vary)
			}
			var out struct{ Errors []string }
			if err := json.NewDecoder(body).Decode(&out); err != nil || len(out.Errors) == 0 {
				t.Errorf("body is not a plain error response: %v", err)
			}
		})
	}
}

// TestSubtreeStreamCompression verifies a gzipped subtree stream sends the
// same events as an identity one, each batch as soon as it is flushed: the
// snapshot arrives while the stream stays open, and each change before the
// next.
func TestSubtreeStreamCompression(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	client := rawClient(t)
	url := base + "/v1/chambers/prod/?subtree=true&watch=true"

	plainResp, plainBody := getEncoded(t, client, url, "")
	gzResp, gzBody := getEncoded(t, client, url, "gzip")
	if ce := plainResp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("identity stream: Content-Encoding %q", ce)
	}
	if ce := gzResp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Errorf("gzip stream: Content-Encoding %q", ce)
	}
	for _, resp := range []*http.Response{plainResp, gzResp} {
		if !varies(resp.Header) {
			t.Errorf("Vary = %q, want Accept-Encoding", resp.Header.Values("Vary"))
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Content-Type = %q", ct)
		}
	}
	plain, gz := sseEvents(plainBody), sseEvents(gzBody)

	// each step's events arrive before the next step is written
	var plainEvents, gzEvents []string
	collect := func(events <-chan sseEvent, n int) []string {
		t.Helper()
		var got []string
		for range n {
			got = append(got, nextEvent(t, events).String())
		}
		return got
	}
	steps := []struct {
		write  func()
		events int
	}{
		{func() {}, 6}, // the snapshot: five views, then synced
		{func() { putChamber(t, base, "prod/tenants/t-2", `{"rules":{"limit":{"type":"number","value":20}}}`) }, 1},
		// every view but loc-1's, which sets beta itself
		{func() {
			putChamber(t, base, "prod", `{"rules":{"limit":{"type":"number","value":1},"beta":{"type":"boolean","value":true}}}`)
		}, 4},
		{func() { putChamber(t, base, "prod/tenants/t-3", `{"rules":{}}`) }, 1},
	}
	for _, s := range steps {
		s.write()
		plainEvents = append(plainEvents, collect(plain, s.events)...)
		gzEvents = append(gzEvents, collect(gz, s.events)...)
	}
	if !slices.Equal(gzEvents, plainEvents) {
		t.Errorf("gzip stream events =\n  %q\nidentity stream events\n  %q", gzEvents, plainEvents)
	}
	if want := "chamber /prod/tenants/t-3/={beta:true limit:1 theme:classic}"; plainEvents[len(plainEvents)-1] != want {
		t.Errorf("last event %q, want %q", plainEvents[len(plainEvents)-1], want)
	}
}

// TestSubtreeStreamHeartbeatsAreFlushed verifies that a gzipped stream's
// heartbeats reach the client without waiting for more.
func TestSubtreeStreamHeartbeatsAreFlushed(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 50 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })

	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	resp, body := getEncoded(t, rawClient(t), base+"/v1/chambers/prod/?subtree=true&watch=true", "gzip")
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding %q", ce)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	pings := 0
	timeout := time.After(5 * time.Second)
	for pings < 2 {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream ended")
			}
			if line == ": ping" {
				pings++
			}
		case <-timeout:
			t.Fatalf("%d heartbeats arrived, want 2", pings)
		}
	}
}

// TestSubtreeStreamGzipEndsCleanly verifies that a gzipped stream that
// reaches its lifetime ends its gzip body, so clients read it to a clean
// end with a matching checksum.
func TestSubtreeStreamGzipEndsCleanly(t *testing.T) {
	old := maxStreamLifetime
	maxStreamLifetime = 300 * time.Millisecond
	t.Cleanup(func() { maxStreamLifetime = old })

	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	_, body := getEncoded(t, rawClient(t), base+"/v1/chambers/prod/?subtree=true&watch=true", "gzip")
	done := make(chan error, 1)
	var text bytes.Buffer
	go func() {
		_, err := io.Copy(&text, body)
		done <- err
	}()
	go func() {
		// a change while the stream is open
		time.Sleep(50 * time.Millisecond)
		putChamber(t, base, "prod/tenants/t-2", `{"rules":{}}`)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the gzip body did not end cleanly: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its lifetime")
	}
	if n := strings.Count(text.String(), "event: synced"); n != 1 {
		t.Errorf("%d synced events in\n%s", n, text.String())
	}
}

// TestGzipStreamStalledReaderLosesStream verifies that write deadlines
// reach the connection through a gzipped stream: a client that stops
// reading loses it.
func TestGzipStreamStalledReaderLosesStream(t *testing.T) {
	old := streamWriteTimeout
	streamWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamWriteTimeout = old })

	// incompressible, so that it fills the socket buffers once compressed
	payload := make([]byte, 4<<20)
	rng := rand.NewChaCha8([32]byte{})
	_, _ = rng.Read(payload)
	ended := make(chan struct{})
	srv := httptest.NewServer(withLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		w, end := compressible(w, r, true)
		defer end()
		conn := startStream(w)
		if conn == nil {
			return
		}
		defer conn.close()
		for i := 0; i < 64 && conn.write(string(payload)); i++ {
		}
	}), logging.New(io.Discard), time.Second))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /v1/chambers/a/?subtree=true&watch=true HTTP/1.1\r\nHost: realm\r\nAccept-Encoding: gzip\r\n\r\n")
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived a client that stopped reading")
	}
}

// TestOtherResponsesAreNotCompressed verifies that only subtree reads and
// watches are gzipped: not single-chamber reads or watches, and not
// evaluation streams, which reflect what the caller sent.
func TestOtherResponsesAreNotCompressed(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "app/", evaluateParent)
	client := rawClient(t)

	for _, path := range []string{"/v1/chambers/app/", "/v1/chambers/app/?watch=true", "/v1/chambers/app/?inherit=layers"} {
		resp, body := getEncoded(t, client, srv.URL+path, "gzip")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		if ce := resp.Header.Get("Content-Encoding"); ce != "" {
			t.Errorf("GET %s: Content-Encoding %q", path, ce)
		}
		// readable as sent
		if line, _ := bufio.NewReader(body).ReadString('\n'); !strings.HasPrefix(line, "{") && !strings.HasPrefix(line, "event: ") {
			t.Errorf("GET %s: body starts %q", path, line)
		}
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evaluate/app/?watch=true", strings.NewReader(`{"attributes":{"tenantId":"acme"}}`))
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ce := resp.Header.Get("Content-Encoding"); resp.StatusCode != http.StatusOK || ce != "" {
		t.Errorf("evaluation stream: %d with Content-Encoding %q", resp.StatusCode, ce)
	}
	if first := waitData(t, dataEvents(bufio.NewScanner(resp.Body)), 2*time.Second); !strings.Contains(first, `"light"`) {
		t.Errorf("evaluation stream's first event = %s", first)
	}
}

// TestGzipWriterChainsBatches verifies that bodies written in batches by
// fresh compressors, flushed or not, decode to what was written, and that
// the gzip trailer checks out.
func TestGzipWriterChainsBatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	word := func() string { return []string{"tenant", "location", "flag", `"x"`, "\n", "{}"}[rng.IntN(6)] }
	tests := []struct {
		name    string
		batches [][]string
	}{
		{"nothing", nil},
		{"an empty flush", [][]string{{}}},
		{"one write", [][]string{{"hello"}}},
		{"several batches", [][]string{{"a", "b"}, {}, {"c"}, {"d", "e", "f"}}},
	}
	var long [][]string
	for range 50 {
		var batch []string
		for range rng.IntN(200) {
			batch = append(batch, strings.Repeat(word(), rng.IntN(300)))
		}
		long = append(long, batch)
	}
	tests = append(tests, struct {
		name    string
		batches [][]string
	}{"many batches", long})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			// a length set before is the identity body's
			rec.Header().Set("Content-Length", "5")
			gz := newGzipWriter(rec, flateWriters)
			var want strings.Builder
			for _, batch := range tt.batches {
				for _, s := range batch {
					if _, err := io.WriteString(gz, s); err != nil {
						t.Fatal(err)
					}
					want.WriteString(s)
				}
				if err := gz.FlushError(); err != nil {
					t.Fatal(err)
				}
				if gz.fw != nil {
					t.Fatal("a flush kept its compressor")
				}
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := gz.Write([]byte("late")); err == nil {
				t.Error("a write after Close succeeded")
			}
			if ce, cl := rec.Header().Get("Content-Encoding"), rec.Header().Get("Content-Length"); ce != "gzip" || cl != "" {
				t.Errorf("Content-Encoding %q, Content-Length %q; want gzip, and no length", ce, cl)
			}
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			zr.Multistream(false)
			got, err := io.ReadAll(zr)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(got) != want.String() {
				t.Errorf("decoded %d bytes, want %d", len(got), want.Len())
			}
			if rest, _ := io.ReadAll(rec.Body); len(rest) != 0 {
				t.Errorf("%d bytes after the gzip body", len(rest))
			}
		})
	}
}

// fanOutProd returns a /prod/ chamber of a plausible size: a dozen flags,
// some targeted, with a request limit of n, so each write of a new n
// changes the view of every chamber below it.
func fanOutProd(n int) string {
	flags := []string{fmt.Sprintf(`"request-limit":{"type":"number","value":%d}`, n)}
	for i := range 6 {
		flags = append(flags, fmt.Sprintf(`"feature-%d":{"type":"boolean","value":false,"targeting":[`+
			`{"clauses":[{"attribute":"service","op":"in","values":["checkout","search","catalog"]},{"attribute":"version","op":"semverGte","values":["2.%d.0"]}],"value":true,"rollout":{"percentage":25}},`+
			`{"clauses":[{"attribute":"environment","op":"in","values":["staging"]}],"value":true}]}`, i, i))
	}
	for i := range 5 {
		flags = append(flags, fmt.Sprintf(`"banner-%d":{"type":"string","value":"Maintenance window %d is announced on the status page; expect brief delays."}`, i, i))
	}
	return `{"rules":{` + strings.Join(flags, ",") + `}}`
}

// seedFanOut writes a /prod/ chamber and, below it, tenants with locations
// of their own, 500 chambers in all, through the API at base.
func seedFanOut(t testing.TB, base string) int {
	t.Helper()
	const tenants, locations = 50, 9
	putChamber(t, base, "prod", fanOutProd(0))
	for i := range tenants {
		putChamber(t, base, fmt.Sprintf("prod/t-%d", i), fmt.Sprintf(`{"rules":{"banner-0":{"type":"string","value":"Tenant %d is migrating this week"},"tenant-name":{"type":"string","value":"Tenant %d"}}}`, i, i))
		for j := range locations {
			putChamber(t, base, fmt.Sprintf("prod/t-%d/l-%d", i, j), fmt.Sprintf(`{"rules":{"feature-0":{"type":"boolean","value":%v}}}`, j%2 == 0))
		}
	}
	return 1 + tenants*(1+locations)
}

// countingConn counts the bytes read from a connection.
type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// BenchmarkSubtreeStreamFanOutBytes measures the bytes on the wire for a
// write to /prod/ that changes the view of each of its 500 descendants, on
// a gzipped subtree stream and on an identity one (wire-B/change); and the
// decoded size (text-B/change).
func BenchmarkSubtreeStreamFanOutBytes(b *testing.B) {
	srv := httptest.NewServer(mustHandler(b))
	b.Cleanup(srv.Close)
	views := seedFanOut(b, srv.URL)
	// each write changes the limit, across runs too
	limit := 0

	for _, encoding := range []string{"identity", "gzip"} {
		b.Run(encoding, func(b *testing.B) {
			var wire atomic.Int64
			tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				return countingConn{Conn: c, n: &wire}, err
			}}
			b.Cleanup(tr.CloseIdleConnections)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/chambers/prod/?subtree=true&watch=true", nil)
			if encoding == "identity" {
				req.Header.Set("Accept-Encoding", "identity")
			} // otherwise the transport asks for gzip, and decodes it
			resp, err := (&http.Client{Transport: tr}).Do(req)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { resp.Body.Close() })
			if got := resp.Header.Get("Content-Encoding") == "gzip" || resp.Uncompressed; got != (encoding == "gzip") {
				b.Fatalf("gzipped = %v", got)
			}
			var text atomic.Int64
			events := sseEvents(io.TeeReader(resp.Body, writerFunc(func(p []byte) { text.Add(int64(len(p))) })))
			for range views + 1 { // the snapshot, then synced
				nextEvent(b, events)
			}

			wire.Store(0)
			text.Store(0)
			b.ResetTimer()
			for range b.N {
				limit++
				putChamber(b, srv.URL, "prod", fanOutProd(limit))
				for range views {
					if ev := nextEvent(b, events); ev.name != "chamber" {
						b.Fatalf("unexpected event %s", ev)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(wire.Load())/float64(b.N), "wire-B/change")
			b.ReportMetric(float64(text.Load())/float64(b.N), "text-B/change")
		})
	}
}

// writerFunc is an io.Writer that hands what is written to a function.
type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) {
	f(p)
	return len(p), nil
}

// mustHandler returns an inheriting handler over a fresh in-memory storage.
func mustHandler(b *testing.B) http.Handler {
	b.Helper()
	inh, err := storage.NewInheritableStorage(newBigCache(b))
	if err != nil {
		b.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: inh})
	if err != nil {
		b.Fatal(err)
	}
	return h
}

// discardWriter is a ResponseWriter that counts and drops what is written.
type discardWriter struct {
	header http.Header
	n      int64
}

func (w *discardWriter) Header() http.Header { return w.header }
func (w *discardWriter) WriteHeader(int)     {}
func (w *discardWriter) Flush()              {}
func (w *discardWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// BenchmarkGzipFanOut measures what one subtree stream spends sending the
// events of a write to /prod/ that changes the view of each of its 500
// descendants (one op), and the bytes it sends (wire-B/op): uncompressed,
// and gzipped at the level realm uses and at others.
func BenchmarkGzipFanOut(b *testing.B) {
	srv := httptest.NewServer(mustHandler(b))
	b.Cleanup(srv.Close)
	seedFanOut(b, srv.URL)
	putChamber(b, srv.URL, "prod", fanOutProd(1))
	// the events a stream sends for the write: one per view
	resp, err := http.Get(srv.URL + "/v1/chambers/prod/?subtree=true")
	if err != nil {
		b.Fatal(err)
	}
	var out struct {
		Data struct {
			Chambers []subtreeEntry `json:"chambers"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	var events []string
	text := 0
	for _, e := range out.Data.Chambers {
		data, err := json.Marshal(subtreeEvent{Path: e.Path, Rules: e.Rules})
		if err != nil {
			b.Fatal(err)
		}
		events = append(events, eventText("chamber", e.Revision, data))
		text += len(events[len(events)-1])
	}

	b.Run("identity", func(b *testing.B) {
		w := &discardWriter{header: http.Header{}}
		b.ReportAllocs()
		for range b.N {
			for _, ev := range events {
				_, _ = io.WriteString(w, ev)
			}
			w.Flush()
		}
		b.ReportMetric(float64(w.n)/float64(b.N), "wire-B/op")
		b.ReportMetric(float64(text), "text-B/op")
	})
	for _, level := range []int{flate.BestSpeed, 2, 4, flate.DefaultCompression, flate.BestCompression} {
		name := fmt.Sprintf("gzip-%d", level)
		if level == flate.DefaultCompression {
			name = "gzip-default"
		}
		if level == gzipLevel {
			name += "-realm"
		}
		b.Run(name, func(b *testing.B) {
			pool := &flatePool{level: level}
			w := &discardWriter{header: http.Header{}}
			gz := newGzipWriter(w, pool)
			b.ReportAllocs()
			for range b.N {
				for _, ev := range events {
					_, _ = io.WriteString(gz, ev)
				}
				gz.Flush()
			}
			b.StopTimer()
			b.ReportMetric(float64(w.n)/float64(b.N), "wire-B/op")
			b.ReportMetric(float64(text), "text-B/op")
		})
	}
}
