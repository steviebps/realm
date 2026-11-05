package realm

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/client"
)

// TestE2EStreamingAgainstServer runs the SDK against a real server at
// REALM_E2E_ADDRESS (see scripts/e2e.sh), which serves the /e2e/ chamber.
func TestE2EStreamingAgainstServer(t *testing.T) {
	addr := os.Getenv("REALM_E2E_ADDRESS")
	if addr == "" {
		t.Skip("REALM_E2E_ADDRESS is not set")
	}
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	rlm, err := NewRealm(WithHttpClient(c), WithPath("/e2e/go/"), WithStreaming(true))
	if err != nil {
		t.Fatal(err)
	}

	put := func(body string) {
		t.Helper()
		res, err := http.Post(addr+"/v1/chambers/e2e/go/", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("put: %s", res.Status)
		}
	}
	put(`{"rules":{"msg":{"type":"string","value":"one"}}}`)

	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	defer rlm.Stop()

	vip := rlm.NewContextWithEvaluation(context.Background(), EvaluationContext{Key: "vip"})
	if got, _ := rlm.Bool(vip, "beta", false); !got {
		t.Error("beta inherited from /e2e/ is not on for vip")
	}

	put(`{"rules":{"msg":{"type":"string","value":"two"}}}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := rlm.String(context.Background(), "msg", "")
		if got == "two" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("streamed change not applied; msg = %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
