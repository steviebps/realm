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

// Bool retrieves a bool by the key of the toggle.
// Returns the default value if it does not exist and a bool on whether or not the toggle exists with that type
func (rlm *Realm) Bool(toggleKey string, defaultValue bool) (bool, error) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.GetValueAt(rlm.applicationVersion).(bool)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// String retrieves a string by the key of the toggle.
// Returns the default value if it does not exist and a bool on whether or not the toggle exists with that type
func (rlm *Realm) String(toggleKey string, defaultValue string) (string, error) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.GetValueAt(rlm.applicationVersion).(string)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// Float64 retrieves a float64 by the key of the toggle.
// Returns the default value if it does not exist and a bool on whether or not the toggle exists with that type
func (rlm *Realm) Float64(toggleKey string, defaultValue float64) (float64, error) {
	c := rlm.getChamber()
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	t := c.Get(toggleKey)
	if t == nil {
		return defaultValue, &ErrToggleNotFound{toggleKey}
	}
	v, ok := t.GetValueAt(rlm.applicationVersion).(float64)
	if !ok {
		return defaultValue, &ErrCouldNotConvertToggle{toggleKey, t.Type}
	}
	return v, nil
}

// CustomValue retrieves an arbitrary value by the key of the toggle
// and unmarshals the value into the custom value v
func (rlm *Realm) CustomValue(toggleKey string, v any) error {
	c := rlm.getChamber()
	if c == nil {
		return ErrChamberEmpty
	}
	t := c.Get(toggleKey)
	if t == nil {
		return &ErrToggleNotFound{toggleKey}
	}
	raw, ok := t.GetValueAt(rlm.applicationVersion).(*json.RawMessage)
	if !ok {
		return fmt.Errorf("could not convert custom toggle %q: it is of type %q", toggleKey, t.Type)
	}
	return json.Unmarshal(*raw, v)
}
