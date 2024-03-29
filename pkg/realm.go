package realm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/utils"
)

type Realm struct {
	applicationVersion string
	path               string
	initSync           sync.Once
	stopCh             chan struct{}
	mu                 sync.RWMutex
	root               *ChamberEntry
	logger             hclog.Logger
	client             *client.Client
	interval           time.Duration
}

type RealmOptions struct {
	Logger             hclog.Logger
	Client             *client.Client
	Path               string
	ApplicationVersion string
	// RefreshInterval is how often realm will refetch the chamber from the realm server
	RefreshInterval time.Duration
}

const (
	// DefaultRefreshInterval is used as the default refresh interval for realm
	DefaultRefreshInterval time.Duration = 15 * time.Minute
)

type contextKey struct {
	name string
}

var (
	// RequestContextKey is the context key to use with a WithValue function to associate a root chamber value with a context
	// such that rule retrievals will be consistent throughout the client's request
	RequestContextKey = &contextKey{"realm"}
)

// NewRealm returns a new Realm struct that carries out all of the core features
func NewRealm(options RealmOptions) (*Realm, error) {
	if options.Client == nil {
		return nil, errors.New("client option must not be nil")
	}
	if options.Path == "" {
		return nil, errors.New("path must not be empty")
	}
	if options.Logger == nil {
		options.Logger = hclog.Default().Named("realm")
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = DefaultRefreshInterval
	}

	return &Realm{
		logger:             options.Logger,
		client:             options.Client,
		path:               options.Path,
		applicationVersion: options.ApplicationVersion,
		stopCh:             make(chan struct{}),
		interval:           options.RefreshInterval,
	}, nil
}

// Start starts realm and initializes the underlying chamber
func (rlm *Realm) Start() error {
	var err error
	rlm.initSync.Do(func() {
		var chamber *Chamber
		if chamber, err = rlm.retrieveChamber(rlm.path); err == nil {
			rlm.setChamber(chamber)
		}
	})

	if err != nil {
		return err
	}

	go func() {
		ticker := time.NewTicker(rlm.interval)
		defer ticker.Stop()
		for {
			select {
			case <-rlm.stopCh:
				rlm.logger.Info("shutting down realm")
				return
			case <-ticker.C:
				if chamber, err := rlm.retrieveChamber(rlm.path); err == nil {
					rlm.setChamber(chamber)
				}
			}
		}
	}()

	return nil
}

// Stop stops realm and flushes any pending tasks
func (rlm *Realm) Stop() {
	close(rlm.stopCh)
}

// Logger retrieves the underlying logger for realm
func (rlm *Realm) Logger() hclog.Logger {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.logger
}

func (rlm *Realm) retrieveChamber(path string) (*Chamber, error) {
	client := rlm.client
	logger := rlm.logger

	res, err := client.PerformRequest("GET", "chambers/"+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		logger.Error(err.Error())
		return nil, err
	}
	defer res.Body.Close()

	var httpRes api.HTTPErrorAndDataResponse
	if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
		logger.Error(fmt.Sprintf("could not read response for getting: %q", path), "error", err.Error())
		return nil, err
	}

	if len(httpRes.Errors) > 0 {
		logger.Error(fmt.Sprintf("could not get %q: %s", path, httpRes.Errors))
		return nil, fmt.Errorf("%s", httpRes.Errors)
	}

	var c Chamber
	err = json.Unmarshal(httpRes.Data, &c)
	if err != nil {
		rlm.logger.Error(err.Error())
		return nil, err
	}

	return &c, nil
}

func (rlm *Realm) setChamber(c *Chamber) {
	entry := NewChamberEntry(c, rlm.applicationVersion)
	rlm.mu.Lock()
	defer rlm.mu.Unlock()
	rlm.root = entry
}

func (rlm *Realm) getChamber() *ChamberEntry {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.root
}

func chamberFromContext(ctx context.Context) *ChamberEntry {
	c, ok := ctx.Value(RequestContextKey).(*ChamberEntry)
	if !ok {
		return nil
	}
	return c
}

func (rlm *Realm) chamberFromContext(ctx context.Context) *ChamberEntry {
	c := chamberFromContext(ctx)
	if c != nil {
		return c
	}
	return rlm.getChamber()
}

func (rlm *Realm) NewContext(ctx context.Context) context.Context {
	c := rlm.getChamber()
	ctx = context.WithValue(ctx, RequestContextKey, c)
	return ctx
}

// Bool retrieves a bool by the key of the rule.
// Returns the default value if it does not exist and a bool on whether or not the rule exists with that type
func (rlm *Realm) Bool(ctx context.Context, ruleKey string, defaultValue bool) (bool, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.BoolValue(ruleKey, defaultValue)
}

// String retrieves a string by the key of the rule.
// Returns the default value if it does not exist and a bool on whether or not the rule exists with that type
func (rlm *Realm) String(ctx context.Context, ruleKey string, defaultValue string) (string, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.StringValue(ruleKey, defaultValue)
}

// Float64 retrieves a float64 by the key of the rule.
// Returns the default value if it does not exist and a bool on whether or not the rule exists with that type
func (rlm *Realm) Float64(ctx context.Context, ruleKey string, defaultValue float64) (float64, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.Float64Value(ruleKey, defaultValue)
}

// CustomValue retrieves an arbitrary value by the key of the rule
// and unmarshals the value into the custom value v
func (rlm *Realm) CustomValue(ctx context.Context, ruleKey string, v any) error {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return ErrChamberEmpty
	}
	err := c.CustomValue(ruleKey, v)
	if err != nil {
		return fmt.Errorf("could not convert custom rule %q: %w", ruleKey, err)
	}
	return nil
}
