package realm

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/utils"
)

type Realm struct {
	applicationVersion string
	path               string
	initSync           sync.Once
	stopCh             chan struct{}
	mu                 sync.RWMutex
	root               *Chamber
	logger             hclog.Logger
	client             *client.Client
	interval           time.Duration
}

type RealmOptions struct {
	Logger             hclog.Logger
	Client             *client.Client
	Path               string
	ApplicationVersion string
	// RefreshInterval is used for how often realm will refetch the underlying chamber from the realm server
	RefreshInterval time.Duration
}

const (
	// DefaultRefreshInterval is used as the default refresh interval for realm
	DefaultRefreshInterval time.Duration = 15 * time.Minute
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
				var chamber *Chamber
				if chamber, err = rlm.retrieveChamber(rlm.path); err == nil {
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

// Client retrieves a realm client
func (rlm *Realm) Client() *client.Client {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.client
}

func (rlm *Realm) retrieveChamber(path string) (*Chamber, error) {
	client := rlm.client
	logger := rlm.logger

	res, err := client.PerformRequest("GET", "/v1/"+path)
	if err != nil {
		logger.Error(err.Error())
		return nil, err
	}
	defer res.Body.Close()

	var or OperationResponse
	if err := utils.ReadInterfaceWith(res.Body, &or); err != nil {
		logger.Error(fmt.Sprintf("could not read response for getting: %q", path), "error", err.Error())
		return nil, err
	}

	if or.Error != "" {
		logger.Error(fmt.Sprintf("could not get %q", path), "error", or.Error)
		return nil, fmt.Errorf(or.Error)
	}

	var c Chamber
	err = json.Unmarshal(or.Data, &c)
	if err != nil {
		rlm.logger.Error(err.Error())
		return nil, err
	}

	return &c, nil
}

func (rlm *Realm) setChamber(c *Chamber) {
	rlm.mu.Lock()
	defer rlm.mu.Unlock()
	rlm.root = c
}

func (rlm *Realm) getChamber() *Chamber {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.root
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, false
	}
	return c.BoolValue(toggleKey, defaultValue, rlm.applicationVersion)
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) StringValue(toggleKey string, defaultValue string) (string, bool) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, false
	}
	return c.StringValue(toggleKey, defaultValue, rlm.applicationVersion)
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, false
	}
	return c.Float64Value(toggleKey, defaultValue, rlm.applicationVersion)
}

// CustomValue retrieves an arbitrary value by the key of the toggle
// and unmarshals the value into the custom value v
func (rlm *Realm) CustomValue(toggleKey string, v any) error {
	c := rlm.getChamber()
	if c == nil {
		return errors.New("root chamber is nil")
	}

	raw, ok := c.CustomValue(toggleKey, rlm.applicationVersion)
	if !ok {
		return fmt.Errorf("could not retrieve custom toggle %q", toggleKey)
	}

	return json.Unmarshal(*raw, v)
}
