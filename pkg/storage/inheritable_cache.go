package storage

import (
	"bytes"
	"sync"

	realm "github.com/steviebps/realm/pkg"
)

// maxParsedBytes bounds the chambers a viewCache keeps parsed, by the size of
// their stored form; the parsed form takes about three times as much again.
const maxParsedBytes = 8 << 20

// maxViewBytes bounds the inherited views a viewCache keeps encoded. A view
// also keeps the chambers it was merged from, which the parsed chambers
// usually hold already.
const maxViewBytes = 8 << 20

// viewCache remembers the chambers InheritableStorage parsed and the inherited
// views it encoded from them, so that reading chambers that have not changed
// neither parses nor encodes them again: an SDK's poll or a browser's
// evaluation would otherwise parse every chamber of the path's lineage.
//
// Nothing is trusted to be current. A parsed chamber is reused only for the
// very bytes it was parsed from, and a view only while every chamber it was
// merged from is the same parsed chamber, so writes need not invalidate
// anything (they may come through other wrappers, or other replicas). Past
// its bounds, the cache forgets entries at random.
//
// Parsed chambers are shared by every read that finds them, so they must not
// be modified; Lineage.Merge never does.
type viewCache struct {
	// maxParsed and maxViews bound parsedBytes and viewBytes.
	maxParsed, maxViews int

	mu          sync.RWMutex
	parsed      map[string]parsedChamber
	parsedBytes int
	views       map[string]encodedView
	viewBytes   int
}

// parsedChamber is a chamber as stored (raw) and as parsed from it.
type parsedChamber struct {
	raw     []byte
	chamber *realm.Chamber
}

// encodedView is an inherited view as encoded, and the chamber of each layer
// it was merged from (nil for a layer without one), nearest first.
type encodedView struct {
	from  []*realm.Chamber
	value []byte
}

// newViewCache returns a viewCache that keeps up to maxParsed bytes of stored
// chambers parsed and up to maxViews bytes of encoded views.
func newViewCache(maxParsed, maxViews int) *viewCache {
	return &viewCache{
		maxParsed: maxParsed,
		maxViews:  maxViews,
		parsed:    map[string]parsedChamber{},
		views:     map[string]encodedView{},
	}
}

// parseLayer returns entry, stored at the canonical path p, as a Layer, as
// ParseLayer does, reusing the chamber last parsed at p when entry holds the
// same bytes.
func (c *viewCache) parseLayer(p string, entry *StorageEntry) Layer {
	c.mu.RLock()
	last, ok := c.parsed[p]
	c.mu.RUnlock()
	if ok && bytes.Equal(last.raw, entry.Value) {
		return Layer{Path: p, Chamber: last.chamber, Version: entry.Version}
	}

	layer := ParseLayer(p, entry)
	size := len(entry.Value)
	if layer.Err != nil || size > c.maxParsed {
		return layer
	}
	raw := bytes.Clone(entry.Value)
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.parsed[p]; ok {
		c.parsedBytes -= len(old.raw)
		delete(c.parsed, p)
	}
	for k, v := range c.parsed {
		if c.parsedBytes+size <= c.maxParsed {
			break
		}
		c.parsedBytes -= len(v.raw)
		delete(c.parsed, k)
	}
	c.parsed[p] = parsedChamber{raw: raw, chamber: layer.Chamber}
	c.parsedBytes += size
	return layer
}

// view returns the encoded view last stored for lin, when lin's layers hold
// the very chambers it was merged from and none has an error.
func (c *viewCache) view(lin *Lineage) ([]byte, bool) {
	c.mu.RLock()
	last, ok := c.views[lin.Layers[0].Path]
	c.mu.RUnlock()
	if !ok || len(last.from) != len(lin.Layers) {
		return nil, false
	}
	for i, l := range lin.Layers {
		if l.Err != nil || l.Chamber != last.from[i] {
			return nil, false
		}
	}
	return last.value, true
}

// storeView remembers value as the encoded view of lin, which merged without
// error. value must not be modified afterwards.
func (c *viewCache) storeView(lin *Lineage, value []byte) {
	size := len(value)
	if size > c.maxViews {
		return
	}
	from := make([]*realm.Chamber, len(lin.Layers))
	for i, l := range lin.Layers {
		from[i] = l.Chamber
	}
	p := lin.Layers[0].Path
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.views[p]; ok {
		c.viewBytes -= len(old.value)
		delete(c.views, p)
	}
	for k, v := range c.views {
		if c.viewBytes+size <= c.maxViews {
			break
		}
		c.viewBytes -= len(v.value)
		delete(c.views, k)
	}
	c.views[p] = encodedView{from: from, value: value}
	c.viewBytes += size
}
