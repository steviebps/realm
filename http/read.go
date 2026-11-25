package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/steviebps/realm/auth"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// chamberReader is the one place chambers are read for requests: a chamber's
// inherited view or the chamber as stored, its evaluation for a context, and
// what watch streams of either are sent. It decides two things every read
// route shares:
//
//   - What a principal may see. Reading a chamber needs CanRead on its path
//     only: its inherited view includes flags from parent chambers the
//     principal may not read, as a key scoped to one chamber must receive
//     what it inherits. Seeing a parent chamber itself (inherit=layers) needs
//     CanRead on each. Why a chamber cannot be parsed is told only to
//     principals that may read it (see readStatus, visibleError).
//   - Which reads are shared. Ordinary requests read storage directly, so a
//     client always sees its own writes, even through another replica.
//     Watch streams, which a write wakes all at once, share their reads and
//     the views built from them (see sharedViews), once per change.
//
// A read's revision (X-Realm-Revision, an event's id) is the highest storage
// revision among the chambers its view is built from (see
// storage.Lineage.Revision); a chamber read as stored carries its own.
type chamberReader struct {
	// strg is the server's storage, whose Get returns inherited views.
	strg storage.Storage
	// views holds the reads watch streams share.
	views *sharedViews
}

// newChamberReader returns the chamberReader of the server's storage strg,
// whose shared reads follow the changes brk announces and take at most
// timeout each.
func newChamberReader(strg storage.Storage, brk *broker, timeout time.Duration) *chamberReader {
	return &chamberReader{strg: strg, views: newSharedViews(strg, brk, timeout)}
}

// Chamber reads the chamber at path: its inherited view, or the chamber as
// stored when own is set. It returns a NotFoundError when no chamber is
// stored at path.
func (cr *chamberReader) Chamber(ctx context.Context, path string, own bool) (*storage.StorageEntry, error) {
	if own {
		return storage.Raw(cr.strg).Get(ctx, path)
	}
	return cr.strg.Get(ctx, path)
}

// evaluation is every flag of a chamber resolved for one context.
type evaluation struct {
	flags map[string]realm.EvaluatedFlag
	// revision is the revision of the view evaluated (0 when the storage
	// does not track revisions).
	revision uint64
	// changesAt is the next time a schedule could change the values, or the
	// zero time when none can.
	changesAt time.Time
}

// Evaluate resolves every flag of the inherited view of the chamber at path
// for ec, from reads that watch streams share when shared is set. It returns
// a NotFoundError when no chamber is stored at path.
func (cr *chamberReader) Evaluate(ctx context.Context, path string, ec realm.EvaluationContext, shared bool) (evaluation, error) {
	var c *realm.Chamber
	var rev uint64
	if shared {
		v, err := cr.views.view(ctx, path)
		if err != nil {
			return evaluation{}, err
		}
		if !v.Stored {
			return evaluation{}, &storage.NotFoundError{Key: path}
		}
		if v.Err != nil {
			return evaluation{}, v.Err
		}
		c, rev = v.Chamber, v.Revision
	} else {
		lin, err := storage.ReadLineage(ctx, cr.strg, path)
		if err != nil {
			return evaluation{}, err
		}
		if c, err = lin.Merge(); err != nil {
			return evaluation{}, err
		}
		rev = lin.Revision()
	}
	flags, changesAt := c.Evaluate(ec)
	return evaluation{flags: flags, revision: rev, changesAt: changesAt}, nil
}

// Watched returns what a watch stream of the chamber at path sends: its
// inherited view, encoded as Chamber returns it, from shared reads. It
// returns a NotFoundError when no chamber is stored at path, so the stream
// waits for one.
func (cr *chamberReader) Watched(ctx context.Context, path string) (streamPayload, error) {
	c, err := cr.views.encoded(ctx, path)
	if err != nil {
		return streamPayload{}, err
	}
	return streamPayload{data: c.data, revision: c.revision}, nil
}

// encodedChamber is a view encoded as InheritableStorage.Get encodes it.
type encodedChamber struct {
	data     []byte
	revision uint64
}

// encodeChamber encodes c as InheritableStorage.Get does, so streams send
// what reads return.
func encodeChamber(c *realm.Chamber) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := utils.WriteInterfaceWith(buf, c, false); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hiddenChamberError is what principals that may not read a chamber that
// cannot be parsed are told instead of why.
const hiddenChamberError = "a chamber it inherits from cannot be parsed"

// mayKnowWhy reports whether p may be told err, why a read failed or a view
// cannot be built: unless it names a chamber that cannot be parsed, which p
// may not read.
func mayKnowWhy(p *auth.Principal, err error) bool {
	var ce *storage.ChamberError
	return !errors.As(err, &ce) || p.CanRead(ce.Path)
}

// visibleError returns what p may be told of err, why a view cannot be
// built (see mayKnowWhy).
func visibleError(p *auth.Principal, err error) string {
	if !mayKnowWhy(p, err) {
		return hiddenChamberError
	}
	return err.Error()
}

// readStatus returns the status of a read that failed with err, and
// whether p may be told why: a chamber that cannot be parsed (the chamber
// read, or one it inherits from) is a server error whose cause p sees only
// when p may read that chamber, so that it can be fixed; other server
// errors are logged, not sent.
func readStatus(p *auth.Principal, err error) (status int, shown bool) {
	var nf *storage.NotFoundError
	var ce *storage.ChamberError
	switch {
	case errors.As(err, &nf), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound, true
	case errors.Is(err, storage.ErrInvalidPath):
		return http.StatusBadRequest, true
	case errors.As(err, &ce):
		return http.StatusInternalServerError, mayKnowWhy(p, err)
	default:
		return http.StatusInternalServerError, false
	}
}

// failRead responds to a read that failed with err as readStatus says.
func failRead(ctx context.Context, w http.ResponseWriter, p *auth.Principal, err error) {
	status, shown := readStatus(p, err)
	span := trace.SpanFromContext(ctx)
	span.SetStatus(codes.Error, err.Error())
	noteFailure(ctx, status, err)
	var nf *storage.NotFoundError
	switch {
	case errors.As(err, &nf):
		handleError(ctx, w, status, createResponseWithErrors(nil, []string{nf.Error()}))
	case errors.Is(err, os.ErrNotExist):
		handleError(ctx, w, status, createResponseWithErrors(nil, []string{http.StatusText(status)}))
	case status == http.StatusInternalServerError && shown:
		// say which chamber is broken and why, as the layers view does;
		// handleError would hide it
		errorCounter.Add(ctx, 1, metric.WithAttributes(attribute.Int("http.status_code", status)))
		handleWithStatus(w, status, createResponseWithErrors(nil, []string{err.Error()}))
	default:
		handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
	}
}
