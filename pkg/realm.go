package realm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"

	"golang.org/x/mod/semver"
)

type Realm struct {
	mu              sync.RWMutex
	root            *Chamber
	logger          hclog.Logger
	storage         storage.Storage
	configPaths     []string
	configName      string
	defaultVersion  string
	configFileToUse string
	configFileUsed  string
}

type RealmOptions struct {
	Storage storage.Storage
	Logger  hclog.Logger
}

// NewRealm returns a new Realm struct that carries out all of the core features
func NewRealm(options RealmOptions) *Realm {
	return &Realm{
		logger:  options.Logger,
		storage: options.Storage,
	}
}

// Logger retrieves the underlying logger for realm
func (rlm *Realm) Logger() hclog.Logger {
	return rlm.logger
}

// Storage retrieves the underlying storage system
func (rlm *Realm) Storage() storage.Storage {
	return rlm.storage
}

// SetVersion sets the version to use for the current config
func (rlm *Realm) SetVersion(version string) error {
	if isValidVersion := semver.IsValid(version); !isValidVersion {
		return fmt.Errorf("%q is not a valid semantic version", version)
	}

	rlm.defaultVersion = version
	return nil
}

// SetConfigFile sets the config file to use when initializing
func (rlm *Realm) SetConfigFile(fileName string) error {
	if fileName == "" {
		return errors.New("file name cannot be empty")
	}

	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	rlm.configFileToUse = fileName
	return nil
}

// SetConfigName sets the config name to look for when initializing
func (rlm *Realm) SetConfigName(fileName string) error {
	if fileName == "" {
		return errors.New("file name cannot be empty")
	}

	if path.Ext(fileName) != ".json" {
		return fmt.Errorf("%q does not have an acceptable file extension. please use .json", fileName)
	}

	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	rlm.configName = fileName
	return nil
}

// AddConfigPath adds a file path to be look for the config when initializing
func (rlm *Realm) AddConfigPath(filePath string) error {
	var fullPath string
	var err error

	if filePath == "" {
		return errors.New("file path cannot be empty")
	}

	if fullPath, err = filepath.Abs(filePath); err != nil {
		return err
	}

	rlm.configPaths = append(rlm.configPaths, fullPath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func (rlm *Realm) ReadInConfig(watch bool) error {
	var err error
	listOfErrors := make([]string, 0)

	if rlm.configFileToUse != "" {
		err = rlm.ReadConfigFile(rlm.configFileToUse)
		if err != nil {
			return err
		}
	} else {
		if len(rlm.configPaths) == 0 {
			return errors.New("could not open config because there were no paths specified. please add a config path")
		}

		if rlm.configName == "" {
			return errors.New("could not open config because there was no name specified specified. please set a config name")
		}

		for _, filePath := range rlm.configPaths {
			fullPath := filepath.Join(filePath, rlm.configName)
			err = rlm.ReadConfigFile(fullPath)
			if err == nil {
				break
			}
			listOfErrors = append(listOfErrors, fmt.Sprintf("%v: %v", fullPath, err))
		}

		if rlm.configFileUsed == "" {
			errStr := ""
			for fullPath, err := range listOfErrors {
				errStr += fmt.Sprintf("%v: %v", fullPath, err)
			}

			return fmt.Errorf("could not open: [ %v ] with file name: %v", strings.Join(listOfErrors, ", "), rlm.configName)
		}
	}

	if watch {
		defer rlm.Watch(rlm.configFileUsed)
	}

	return nil
}

// readIntoRootChamber reads from the passed reader into the root Chamber
func (rlm *Realm) readIntoRootChamber(r io.Reader) error {
	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	var root Chamber
	if err := utils.ReadInterfaceWith(r, &root); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	rlm.root = &root
	return nil
}

func (rlm *Realm) ReadConfigFile(fileName string) error {
	rc, err := utils.OpenLocalConfig(fileName)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := rlm.readIntoRootChamber(rc); err != nil {
		return err
	}

	rlm.configFileUsed = fileName
	return nil
}

func (rlm *Realm) Watch(fileName string) {
	if fileName == "" {
		return
	}

	init := make(chan interface{})
	go func() {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			init <- struct{}{}
			return
		}
		defer watcher.Close()

		events := make(chan interface{})
		go func() {

			for {
				select {
				case event, ok := <-watcher.Events:
					if !ok {
						events <- struct{}{}
						return
					}

					const writeOrCreateMask = fsnotify.Write | fsnotify.Create
					if event.Op&writeOrCreateMask != 0 {
						err := rlm.ReadConfigFile(fileName)
						if err != nil {
							rlm.logger.Error(fmt.Sprintf("could not re-read config file: %v", err))
						} else {
							rlm.logger.Info(fmt.Sprintf("refreshing config file: %v", fileName))
						}
					}
				case err, ok := <-watcher.Errors:
					if ok {
						rlm.logger.Error(err.Error())
					}
					events <- struct{}{}
					return
				}
			}
		}()

		watcher.Add(fileName)
		init <- struct{}{}
		<-events
		rlm.logger.Warn(fmt.Sprintf("file is no longer being watched: %q", fileName))
	}()
	<-init
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	if rlm.root == nil {
		return defaultValue, false
	}
	return rlm.root.BoolValue(toggleKey, defaultValue, rlm.defaultVersion)
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) StringValue(toggleKey string, defaultValue string) (string, bool) {
	if rlm.root == nil {
		return defaultValue, false
	}
	return rlm.root.StringValue(toggleKey, defaultValue, rlm.defaultVersion)
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func (rlm *Realm) Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	if rlm.root == nil {
		return defaultValue, false
	}
	return rlm.root.Float64Value(toggleKey, defaultValue, rlm.defaultVersion)
}

// CustomValue retrieves an arbitrary value by the key of the toggle
func (rlm *Realm) CustomValue(toggleKey string, v any) error {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()

	t := rlm.root.GetToggle(toggleKey)
	if t == nil {
		return fmt.Errorf("could not find toggle with this key: %s", toggleKey)
	}

	raw, ok := t.GetValueAt(rlm.defaultVersion).(*json.RawMessage)
	if !ok {
		return fmt.Errorf("could not convert data type to be unmarshaled: %s", toggleKey)
	}

	return json.Unmarshal(*raw, v)
}
