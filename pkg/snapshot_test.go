package realm

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/client"
)

// TestUnsupportedFlagsLoggedOnce checks that a chamber snapshot warns about
// each flag this SDK cannot fully evaluate once, as a subtree snapshot does,
// however often the chamber changes.
func TestUnsupportedFlagsLoggedOnce(t *testing.T) {
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	rlm, err := NewRealm(WithHttpClient(c), WithPath("/a/"), WithLogger(zerolog.New(&logs)))
	if err != nil {
		t.Fatal(err)
	}
	decode := func(s string) *Chamber {
		t.Helper()
		ch, err := decodeChamber([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	future := decode(`{"rules":{"later":{"type":"futureType","value":1}}}`)
	plain := decode(`{"rules":{"now":{"type":"boolean","value":true}}}`)
	both := decode(`{"rules":{"later":{"type":"futureType","value":1},"other":{"type":"futureType","value":2}}}`)

	snap := rlm.snap.(*chamberSnapshot)
	ctx := context.Background()
	for _, ch := range []*Chamber{future, plain, future, both} {
		snap.set(ctx, ch, 0)
	}
	warnings := strings.Count(logs.String(), "does not support")
	if warnings != 2 {
		t.Fatalf("%d warnings, want 2 (later, then other):\n%s", warnings, logs.String())
	}
	if strings.Count(logs.String(), `"later"`) != 1 || strings.Count(logs.String(), `"other"`) != 1 {
		t.Errorf("each flag should be named once:\n%s", logs.String())
	}
	if got := rlm.ChamberPath(ctx); got != "/a/" {
		t.Errorf("ChamberPath = %q, want the configured path", got)
	}
}
