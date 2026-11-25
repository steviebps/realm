package realm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// streamCase is one case of sdk/conformance/stream.json.
type streamCase struct {
	Name   string            `json:"name"`
	Held   []json.RawMessage `json:"held"`
	Events []struct {
		Event string          `json:"event"`
		ID    string          `json:"id"`
		Data  json.RawMessage `json:"data"`
	} `json:"events"`
	Views     map[string]string `json:"views"`
	Revisions map[string]uint64 `json:"revisions"`
}

// eventData returns the data an event of the case file sends: a string as
// written, anything else as JSON.
func eventData(raw json.RawMessage) []byte {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s)
	}
	return raw
}

// TestStreamConformance applies the subtree stream cases the TypeScript SDK
// runs too (sdk/typescript/test/subtree.test.ts).
func TestStreamConformance(t *testing.T) {
	b, err := os.ReadFile("../sdk/conformance/stream.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []streamCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			rlm := templateRealm(t, "/r/{t}/", nil)
			snap := subtreeOf(rlm)

			held := make([]subtreeView, 0, len(tc.Held))
			for _, raw := range tc.Held {
				var e subtreeEntry
				if err := json.Unmarshal(raw, &e); err != nil {
					t.Fatal(err)
				}
				v, err := e.decode()
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, v)
			}
			snap.setIf(ctx, held, rlm.snapshotVersion())

			st := newSubtreeStream(snap)
			for _, ev := range tc.Events {
				// an event the SDK cannot use is skipped; only the views count
				_, _ = st.event(ctx, ev.Event, ev.ID, eventData(ev.Data))
			}

			rlm.mu.RLock()
			defer rlm.mu.RUnlock()
			got := make(map[string]string, len(snap.views))
			for p, v := range snap.views {
				msg, _ := v.rules["msg"].Value.(string)
				got[p] = msg
			}
			if len(got) != len(tc.Views) {
				t.Errorf("views = %v, want %v", got, tc.Views)
			}
			for p, want := range tc.Views {
				if got[p] != want {
					t.Errorf("view %s = %q, want %q (views %v)", p, got[p], want, got)
				}
			}
			for p, want := range tc.Revisions {
				if v := snap.views[p]; v == nil || v.revision != want {
					t.Errorf("revision of %s = %v, want %d", p, v, want)
				}
			}
		})
	}
}
