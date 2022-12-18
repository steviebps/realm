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
	"github.com/steviebps/realm/utils"
	"golang.org/x/mod/semver"
)

type config struct {
	mu              sync.RWMutex
	root            *Chamber
	path            string
	configPaths     []string
	configName      string
	defaultVersion  string
	configFileToUse string
	configFileUsed  string
}

var c *config

func init() {
	c = &config{}
}

// SetVersion sets the version to use for the current config
func SetVersion(version string) error { return c.SetVersion(version) }

func (cfg *config) SetVersion(version string) error {
	if isValidVersion := semver.IsValid(version); !isValidVersion {
		return fmt.Errorf("%q is not a valid semantic version", version)
	}

	cfg.defaultVersion = version
	return nil
}

// SetConfigFile sets the config file to use when initializing
func SetConfigFile(fileName string) error { return c.SetConfigFile(fileName) }

func (cfg *config) SetConfigFile(fileName string) error {
	if fileName == "" {
		return errors.New("file name cannot be empty")
	}

	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	cfg.configFileToUse = fileName
	return nil
}

// SetConfigName sets the config name to look for when initializing
func SetConfigName(fileName string) error { return c.SetConfigName(fileName) }

func (cfg *config) SetConfigName(fileName string) error {
	if fileName == "" {
		return errors.New("file name cannot be empty")
	}

	if path.Ext(fileName) != ".json" {
		return fmt.Errorf("%q does not have an acceptable file extension. please use JSON", fileName)
	}

	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	cfg.configName = fileName
	return nil
}

// AddConfigPath adds a file path to be look for the config when initializing
func AddConfigPath(filePath string) error { return c.AddConfigPath(filePath) }

func (cfg *config) AddConfigPath(filePath string) error {
	var fullPath string
	var err error

	if filePath == "" {
		return errors.New("file path cannot be empty")
	}

	if fullPath, err = filepath.Abs(filePath); err != nil {
		return err
	}

	cfg.configPaths = append(c.configPaths, fullPath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func ReadInConfig(watch bool) error { return c.ReadInConfig(watch) }

func (cfg *config) ReadInConfig(watch bool) error {
	var err error
	listOfErrors := make([]string, 0)

	if cfg.configFileToUse != "" {
		err = cfg.ReadConfigFile(cfg.configFileToUse)
		if err != nil {
			return err
		}
	} else {
		if len(cfg.configPaths) == 0 {
			return errors.New("could not open config because there were no paths specified. please add a config path")
		}

		if cfg.configName == "" {
			return errors.New("could not open config because there was no name specified specified. please set a config name")
		}

		for _, filePath := range cfg.configPaths {
			fullPath := filepath.Join(filePath, cfg.configName)
			err = cfg.ReadConfigFile(fullPath)
			if err == nil {
				break
			}
			listOfErrors = append(listOfErrors, fmt.Sprintf("%v: %v", fullPath, err))
		}

		if cfg.configFileUsed == "" {
			errStr := ""
			for fullPath, err := range listOfErrors {
				errStr += fmt.Sprintf("%v: %v", fullPath, err)
			}

			return fmt.Errorf("could not open: [ %v ] with file name: %v", strings.Join(listOfErrors, ", "), cfg.configName)
		}
	}

	if watch {
		defer cfg.Watch(cfg.configFileUsed)
	}

	return nil
}

func (cfg *config) ReadChamber(r io.Reader, fileName string) error {
	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	var root Chamber
	if err := utils.ReadInterfaceWith(r, &root); err != nil {
		return fmt.Errorf("error reading file %q: %w", fileName, err)
	}

	cfg.root = &root
	cfg.configFileUsed = fileName

	return nil
}

func (cfg *config) ReadConfigFile(fileName string) error {
	rc, err := utils.OpenLocalConfig(fileName)
	if err != nil {
		return err
	}
	defer rc.Close()

	return cfg.ReadChamber(rc, fileName)
}

func (cfg *config) Watch(fileName string) {
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
						err := cfg.ReadConfigFile(fileName)
						if err != nil {
							fmt.Printf("could not re-read config file: %v\n", err)
						} else {
							fmt.Printf("refreshing config file: %v\n", fileName)
						}
					}
				case err, ok := <-watcher.Errors:
					if ok {
						fmt.Printf("error: %v\n", err)
					}
					events <- struct{}{}
					return
				}
			}
		}()

		watcher.Add(fileName)
		init <- struct{}{}
		<-events
		fmt.Printf("done watching file: %q\n", fileName)
	}()
	<-init
}

func SetPath(path string) {
	c.SetPath(path)
}

func (cfg *config) SetPath(path string) {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cfg.path = path
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	if c.root == nil {
		return defaultValue, false
	}
	return c.root.BoolValue(toggleKey, defaultValue, c.defaultVersion)
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func StringValue(toggleKey string, defaultValue string) (string, bool) {
	if c.root == nil {
		return defaultValue, false
	}
	return c.root.StringValue(toggleKey, defaultValue, c.defaultVersion)
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	if c.root == nil {
		return defaultValue, false
	}
	return c.root.Float64Value(toggleKey, defaultValue, c.defaultVersion)
}

// CustomValue retrieves an arbitrary value by the key of the toggle
func CustomValue(toggleKey string, v any) error {
	return c.CustomValue(toggleKey, v)
}

func (cfg *config) CustomValue(toggleKey string, v any) error {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()

	t := cfg.root.GetToggle(toggleKey)
	if t == nil {
		return fmt.Errorf("could not find toggle with this key: %s", toggleKey)
	}

	raw, ok := t.GetValueAt(cfg.defaultVersion).(*json.RawMessage)
	if !ok {
		return fmt.Errorf("could not convert data type to be unmarshaled: %s", toggleKey)
	}

	return json.Unmarshal(*raw, v)
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }
