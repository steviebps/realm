package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/helper/logging"
)

// TestBigCacheConfig checks how life_window and clean_window are read: as
// primary storage, entries are kept unless a life_window is set (with a
// warning); as cacheable's cache, they expire after DefaultBigCacheLifeWindow.
func TestBigCacheConfig(t *testing.T) {
	tests := []struct {
		name      string
		conf      map[string]string
		primary   bool
		wantLife  time.Duration
		wantClean time.Duration
		wantWarn  bool
		wantErr   bool
	}{
		{name: "primary keeps entries", conf: map[string]string{}, primary: true, wantLife: bigCacheNoExpiry, wantClean: 0},
		{name: "primary with life_window 0", conf: map[string]string{"life_window": "0"}, primary: true, wantLife: bigCacheNoExpiry, wantClean: 0},
		{name: "primary with a life_window warns", conf: map[string]string{"life_window": "24h"}, primary: true, wantLife: 24 * time.Hour, wantClean: DefaultBigCacheCleanWindow, wantWarn: true},
		{name: "integer nanoseconds", conf: map[string]string{"life_window": "86400000000000", "clean_window": "1000000000"}, primary: true, wantLife: 24 * time.Hour, wantClean: time.Second, wantWarn: true},
		{name: "cache tier expires by default", conf: map[string]string{}, wantLife: DefaultBigCacheLifeWindow, wantClean: DefaultBigCacheCleanWindow},
		{name: "cache tier with a duration", conf: map[string]string{"life_window": "90s", "clean_window": "10s"}, wantLife: 90 * time.Second, wantClean: 10 * time.Second},
		{name: "cache tier that keeps entries", conf: map[string]string{"life_window": "0"}, wantLife: bigCacheNoExpiry, wantClean: 0},
		{name: "explicit clean_window without expiry", conf: map[string]string{"clean_window": "1m"}, primary: true, wantLife: bigCacheNoExpiry, wantClean: time.Minute},
		{name: "window under a second", conf: map[string]string{"life_window": "500ms"}, primary: true, wantErr: true},
		{name: "bare small integer", conf: map[string]string{"life_window": "300"}, wantErr: true},
		{name: "negative", conf: map[string]string{"life_window": "-5m"}, primary: true, wantErr: true},
		{name: "unparsable", conf: map[string]string{"clean_window": "soon"}, primary: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaultLife := time.Duration(0)
			if !tt.primary {
				defaultLife = DefaultBigCacheLifeWindow
			}
			cfg, warning, err := bigCacheConfig(tt.conf, defaultLife, tt.primary)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("bigCacheConfig(%v) succeeded, want an error", tt.conf)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LifeWindow != tt.wantLife || cfg.CleanWindow != tt.wantClean {
				t.Errorf("LifeWindow, CleanWindow = %v, %v; want %v, %v", cfg.LifeWindow, cfg.CleanWindow, tt.wantLife, tt.wantClean)
			}
			if (warning != "") != tt.wantWarn {
				t.Errorf("warning = %q, want one: %v", warning, tt.wantWarn)
			}
		})
	}
}

// TestBigCachePrimaryKeepsEntries writes to primary bigcache storage with the
// default options and with a one-second life_window, and checks that only
// the latter loses its entries, however often they are read.
func TestBigCachePrimaryKeepsEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for entries to expire")
	}
	ctx := context.Background()
	keep, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	defer keep.Close(ctx)
	expire, err := NewBigCacheStorage(map[string]string{"life_window": "1s", "clean_window": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	defer expire.Close(ctx)

	keys := []string{"/", "/_realm/keys/"}
	for _, s := range []Storage{keep, expire} {
		for _, k := range keys {
			if err := s.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		_, _ = keep.Get(ctx, "/")
		_, _ = expire.Get(ctx, "/")
	}
	for _, k := range keys {
		if _, err := keep.Get(ctx, k); err != nil {
			t.Errorf("default primary storage lost %s: %v", k, err)
		}
		if _, err := expire.Get(ctx, k); !isNotFound(err) {
			t.Errorf("storage with a 1s life_window kept %s (%v)", k, err)
		}
	}
}

// TestBigCacheWarnsOnce checks that primary bigcache storage with a
// life_window warns once, with the first operation's logger, and that
// cacheable's bigcache cache does not.
func TestBigCacheWarnsOnce(t *testing.T) {
	for _, tt := range []struct {
		name   string
		create StorageCreator
		conf   map[string]string
		want   int
	}{
		{"primary with a life_window", NewBigCacheStorage, map[string]string{"life_window": "1h"}, 1},
		{"primary without one", NewBigCacheStorage, map[string]string{}, 0},
		{"cache tier", newBigCacheTier, map[string]string{"life_window": "1h"}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := logging.New(&buf).WithContext(context.Background())
			s, err := tt.create(tt.conf)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(ctx)
			_, _ = s.Get(ctx, "/")
			_ = s.Put(ctx, StorageEntry{Key: "/", Value: json.RawMessage(`{}`)})
			if got := strings.Count(buf.String(), "bigcache storage deletes every entry"); got != tt.want {
				t.Errorf("logged %d warnings, want %d:\n%s", got, tt.want, buf.String())
			}
		})
	}
}

// TestBigCacheLifeWindowByRole checks which life window each way of creating
// bigcache storage ends up with: primary storage (as --dev and
// "storage": "bigcache" create it) keeps entries, while cacheable's bigcache
// cache expires them, so it rereads what other processes change.
func TestBigCacheLifeWindowByRole(t *testing.T) {
	ctx := context.Background()
	primary, err := StorageOptions["bigcache"](map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close(ctx)
	c, err := NewCacheableStorageWithConf(map[string]string{"cache": "bigcache", "source": "file", "path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tier, ok := c.(*CacheableStorage).cache.(*BigCacheStorage)
	if !ok {
		t.Fatalf("cacheable's cache is %T, want *BigCacheStorage", c.(*CacheableStorage).cache)
	}

	if got := primary.(*BigCacheStorage).lifeWindow; got != bigCacheNoExpiry {
		t.Errorf("primary bigcache life window = %v, want entries kept", got)
	}
	if got := tier.lifeWindow; got != DefaultBigCacheLifeWindow {
		t.Errorf("cacheable's bigcache life window = %v, want %v", got, DefaultBigCacheLifeWindow)
	}
	if _, err := NewCacheableStorageWithConf(map[string]string{"source": "file", "path": t.TempDir(), "life_window": "nonsense"}); err == nil {
		t.Error("cacheable accepted an invalid life_window for its cache")
	}
}
