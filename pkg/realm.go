package realm

import (
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/steviebps/realm/utils"
	"golang.org/x/mod/semver"
)

type config struct {
	mu              sync.RWMutex
	rootChamber     *Chamber
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
	if filePath == "" {
		return errors.New("file path cannot be empty")
	}

	cfg.configPaths = append(c.configPaths, filePath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func ReadInConfig(watch bool) error { return c.ReadInConfig(watch) }

func (cfg *config) ReadInConfig(watch bool) error {
	var err error

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
			fullPath := filepath.Join(filePath + cfg.configName)
			err = cfg.ReadConfigFile(fullPath)
			if err == nil {
				break
			}
		}

		if cfg.configFileUsed == "" {
			return fmt.Errorf("could not open any of the config paths: %v with this name: %v", cfg.configPaths, cfg.configName)
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
	var err error

	if err = utils.ReadInterfaceWith(r, &root); err != nil {
		return fmt.Errorf("error reading file %q: %w", fileName, err)
	}

	cfg.rootChamber = &root
	cfg.configFileUsed = fileName

	return nil
}

func (cfg *config) ReadConfigFile(fileName string) error {
	rc, err := utils.OpenLocalConfig(fileName)
	if err != nil {
		return fmt.Errorf("error opening file %q: %w", fileName, err)
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
		fmt.Println("done watching file")
	}()
	<-init
}

// BoolValue retrieves a bool by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	return c.BoolValue(toggleKey, defaultValue)
}

func (cfg *config) BoolValue(toggleKey string, defaultValue bool) (bool, bool) {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cBool, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(bool)
	if !ok {
		return defaultValue, false
	}

	return cBool, true
}

// StringValue retrieves a string by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func StringValue(toggleKey string, defaultValue string) (string, bool) {
	return c.StringValue(toggleKey, defaultValue)
}

func (cfg *config) StringValue(toggleKey string, defaultValue string) (string, bool) {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cStr, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(string)
	if !ok {
		return defaultValue, false
	}

	return cStr, true
}

// Float64Value retrieves a float64 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
func Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	return c.Float64Value(toggleKey, defaultValue)
}

func (cfg *config) Float64Value(toggleKey string, defaultValue float64) (float64, bool) {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cFloat64, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float64)
	if !ok {
		return defaultValue, false
	}

	return cFloat64, true
}

// Float32Value retrieves a float32 by the key of the toggle
// and returns the default value if it does not exist and a bool on whether or not the toggle exists
// if the config value overflows the type requested, defaultValue will be returned
func Float32Value(toggleKey string, defaultValue float32) (float32, bool) {
	return c.Float32Value(toggleKey, defaultValue)
}

func (cfg *config) Float32Value(toggleKey string, defaultValue float32) (float32, bool) {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cFloat32, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float32)
	if !ok {
		return defaultValue, false
	}

	return cFloat32, true
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }
