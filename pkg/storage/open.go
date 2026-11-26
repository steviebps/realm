package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/steviebps/realm/helper/logging"
)

// emptyChamber is the JSON of a chamber with no flags.
var emptyChamber = []byte(`{"rules":{}}`)

// Open creates the storage a server serves chambers from: the backend
// registered as typ in StorageOptions, created with options, wrapped for
// inheritance when inheritable (outermost, as the server's reads and writes
// expect: see Raw), with the root chamber created unless it exists, so that
// every chamber has one to inherit from. Servers starting together against
// shared storage create it once. Closing what Open returns closes the
// backend; Open closes it itself when it fails after creating it.
func Open(ctx context.Context, typ string, options map[string]string, inheritable bool) (Storage, error) {
	create, exists := StorageOptions[typ]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", typ)
	}
	backend, err := create(options)
	if err != nil {
		return nil, err
	}
	s := backend
	if inheritable {
		if s, err = NewInheritableStorage(backend); err != nil {
			return nil, errors.Join(err, backend.Close(ctx))
		}
	}
	err = Update(ctx, s, []string{"/"}, func(cur map[string]*StorageEntry) ([]Op, error) {
		if cur["/"] != nil {
			return nil, nil
		}
		logging.Ctx(ctx).DebugCtx(ctx).Msg("initializing root entry in storage")
		return []Op{{Key: "/", Value: emptyChamber}}, nil
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to initialize root entry during startup: %w", err), s.Close(ctx))
	}
	return s, nil
}
