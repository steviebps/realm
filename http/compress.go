package http

import (
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipLevel is the level subtree reads and watches are compressed at.
// Every subtree stream compresses its own copy of each event, so CPU
// matters more than the last few bytes: on the events of a write high in
// the tree, whose views repeat each other, flate.BestSpeed shrinks them
// about 65 times, level 2 about 85 times for half again the CPU, and the
// default level about 100 times for three times the CPU (see
// BenchmarkGzipFanOut).
const gzipLevel = flate.BestSpeed

// gzipHeader begins each gzip body realm writes (RFC 1952): deflate, no
// name, no modification time, unknown operating system.
var gzipHeader = []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}

// flatePool keeps deflate compressors at one level for reuse. Each holds
// about half a megabyte, so a body borrows one only while it writes (see
// gzipWriter).
type flatePool struct {
	level int
	pool  sync.Pool
}

// flateWriters are the compressors at gzipLevel.
var flateWriters = &flatePool{level: gzipLevel}

// get returns a compressor writing to w.
func (p *flatePool) get(w io.Writer) *flate.Writer {
	if fw, ok := p.pool.Get().(*flate.Writer); ok {
		fw.Reset(w)
		return fw
	}
	fw, err := flate.NewWriter(w, p.level)
	if err != nil {
		panic(err) // the level is a constant
	}
	return fw
}

// put returns fw for reuse, without the writer it wrote to.
func (p *flatePool) put(fw *flate.Writer) {
	fw.Reset(io.Discard)
	p.pool.Put(fw)
}

// acceptsGzip reports whether r's Accept-Encoding allows a gzip response
// (RFC 9110, section 12.5.3): gzip (or x-gzip) listed with a nonzero
// weight, or * with a nonzero weight while gzip is not listed.
func acceptsGzip(r *http.Request) bool {
	gzip, star := -1.0, -1.0
	for _, field := range r.Header.Values("Accept-Encoding") {
		for item := range strings.SplitSeq(field, ",") {
			coding, params, _ := strings.Cut(item, ";")
			q := 1.0
			for param := range strings.SplitSeq(params, ";") {
				name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
					continue
				}
				var err error
				if q, err = strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil || q < 0 || q > 1 {
					q = 0 // a weight that does not parse allows nothing
				}
			}
			switch coding = strings.ToLower(strings.TrimSpace(coding)); coding {
			case "gzip", "x-gzip":
				gzip = max(gzip, q)
			case "*":
				star = max(star, q)
			}
		}
	}
	if gzip >= 0 {
		return gzip > 0
	}
	return star > 0
}

// compressible returns w, gzipped when enabled and r accepts gzip, and a
// function that ends the response body, which the caller calls once it is
// done writing. It marks the response gzipped, so the caller calls it once
// nothing can fail before the body starts, and responds to errors before
// that with w, uncompressed. Responses that may be compressed vary by
// Accept-Encoding, which the caller reports with varyByEncoding, whether or
// not this one is compressed.
func compressible(w http.ResponseWriter, r *http.Request, enabled bool) (http.ResponseWriter, func()) {
	if !enabled || !acceptsGzip(r) {
		return w, func() {}
	}
	gz := newGzipWriter(w, flateWriters)
	return gz, func() { _ = gz.Close() }
}

// contentEncoding names the coding of what w writes: gzip, for a writer
// compressible returned compressed, or identity.
func contentEncoding(w http.ResponseWriter) string {
	if _, ok := w.(*gzipWriter); ok {
		return "gzip"
	}
	return "identity"
}

// varyByEncoding says that the response to the request depends on its
// Accept-Encoding, as every response that compressible may compress does,
// errors included.
func varyByEncoding(w http.ResponseWriter) {
	w.Header().Add("Vary", "Accept-Encoding")
}

// gzipWriter compresses a response with gzip, and makes each flush send
// what was written so far (a sync flush), so streams stay prompt.
//
// It does not hold a compressor between flushes: a stream borrows one from
// a pool for each batch of events it writes, and returns it when it
// flushes. A batch is a run of deflate blocks ending on a byte boundary,
// so the batches of fresh compressors chain into one deflate stream (RFC
// 1951), whose length and checksum gzipWriter keeps. That keeps an idle
// stream's memory to a few words instead of a compressor's half megabyte;
// what it costs is that a batch cannot refer back to an earlier batch,
// which matters little, as deflate refers back at most 32 KiB anyway.
//
// It unwraps for http.ResponseController, so write deadlines reach the
// connection.
type gzipWriter struct {
	http.ResponseWriter
	pool *flatePool
	// fw is the compressor of the current batch; nil between batches.
	fw *flate.Writer
	// crc and size are the CRC-32 and the length, modulo 2^32, of what was
	// written, for the gzip trailer.
	crc  uint32
	size uint32
	// started reports that the gzip header was written.
	started bool
	closed  bool
	// err is the first error writing the body, after which writes fail.
	err error
}

// newGzipWriter returns a gzipWriter compressing to w with compressors from
// pool. It marks the response gzipped; the caller must not have written its
// status yet.
func newGzipWriter(w http.ResponseWriter, pool *flatePool) *gzipWriter {
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	// the length of the compressed body is not known in advance
	h.Del("Content-Length")
	return &gzipWriter{ResponseWriter: w, pool: pool}
}

// errGzipClosed reports a write after the gzip body ended.
var errGzipClosed = errors.New("the gzip body has ended")

// begin writes the gzip header unless it was written, and borrows a
// compressor for a batch unless one is borrowed.
func (g *gzipWriter) begin() error {
	if g.err != nil {
		return g.err
	}
	if g.closed {
		return errGzipClosed
	}
	if !g.started {
		g.started = true
		if _, err := g.ResponseWriter.Write(gzipHeader); err != nil {
			g.err = err
			return err
		}
	}
	if g.fw == nil {
		g.fw = g.pool.get(g.ResponseWriter)
	}
	return nil
}

// Write compresses p into the current batch.
func (g *gzipWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, g.err
	}
	if err := g.begin(); err != nil {
		return 0, err
	}
	g.crc = crc32.Update(g.crc, crc32.IEEETable, p)
	// gzip records the length modulo 2^32
	g.size += uint32(len(p))
	n, err := g.fw.Write(p)
	if err != nil {
		g.err = err
	}
	return n, err
}

// endBatch ends the current batch, if any, on a byte boundary (a sync
// flush), and returns its compressor to the pool.
func (g *gzipWriter) endBatch() {
	if g.fw == nil {
		return
	}
	if err := g.fw.Flush(); err != nil && g.err == nil {
		g.err = err
	}
	g.pool.put(g.fw)
	g.fw = nil
}

// FlushError sends what was written so far: it ends the current batch, then
// flushes the response (http.ResponseController.Flush calls it).
func (g *gzipWriter) FlushError() error {
	if !g.closed {
		if err := g.begin(); err != nil {
			return err
		}
		g.endBatch()
		if g.err != nil {
			return g.err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

// Flush is FlushError without its error, for http.Flusher.
func (g *gzipWriter) Flush() { _ = g.FlushError() }

// Close ends the gzip body: a final deflate block, then the trailer. It
// does not flush the response; the server does once the handler returns.
func (g *gzipWriter) Close() error {
	if g.closed {
		return g.err
	}
	if err := g.begin(); err == nil {
		// the final block ends the deflate stream
		if err := g.fw.Close(); err != nil {
			g.err = err
		}
	}
	if g.fw != nil {
		g.pool.put(g.fw)
		g.fw = nil
	}
	g.closed = true
	if g.err != nil {
		return g.err
	}
	var trailer [8]byte
	binary.LittleEndian.PutUint32(trailer[:4], g.crc)
	binary.LittleEndian.PutUint32(trailer[4:], g.size)
	_, g.err = g.ResponseWriter.Write(trailer[:])
	return g.err
}

// Unwrap returns the response gzipWriter compresses to, for
// http.ResponseController.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
