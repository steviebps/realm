package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/steviebps/rein/internal/logger"
	"golang.org/x/mod/semver"
)

type config struct {
	mu             sync.RWMutex
	rootChamber    Chamber
	configPaths    []string
	defaultVersion string
	configFileUsed string
}

var c *config

func init() {
	c = newConfig()
}

func newConfig() *config {
	return &config{}
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

// AddConfigPath adds a file path to be look for the config when initializing
func AddConfigPath(path string) error { return c.AddConfigPath(path) }

func (cfg *config) AddConfigPath(filePath string) error {
	if filePath == "" {
		return errors.New("config path cannot be empty")
	}

	if path.Ext(filePath) != ".json" {
		return fmt.Errorf("%q does not have an acceptable file extension. Please use JSON", filePath)
	}

	cfg.configPaths = append(c.configPaths, filePath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func ReadInConfig(watch bool) error { return c.ReadInConfig(watch) }

func (cfg *config) ReadInConfig(watch bool) error {
	var rc io.ReadCloser
	var err error

	for _, fileName := range cfg.configPaths {
		rc, err = openLocalConfig(fileName)
		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error opening file: %v", err))
			continue
		}
		defer rc.Close()
		cfg.configFileUsed = fileName
		break
	}

	if rc == nil {
		return fmt.Errorf("could not open any of the config paths: %v", cfg.configPaths)
	}

	if watch {
		defer cfg.Watch()
	}
	return cfg.ReadConfig(rc)
}

func (cfg *config) ReadConfig(r io.Reader) error {
	var root Chamber
	byteValue, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("error reading file %q: %w", cfg.configFileUsed, err)
	}

	if err := json.Unmarshal(byteValue, &root); err != nil {
		return fmt.Errorf("error reading file %q: %w", cfg.configFileUsed, err)
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	cfg.rootChamber = root

	return nil
}

func (cfg *config) ReadConfigFileUsed() error {
	rc, err := openLocalConfig(cfg.configFileUsed)
	if err != nil {
		return fmt.Errorf("error opening file %q: %w", cfg.configFileUsed, err)
	}
	defer rc.Close()

	return cfg.ReadConfig(rc)
}

func (cfg *config) Watch() error {
	if cfg.configFileUsed == "" {
		return errors.New("a config file was not successfully read so it cannot be watched")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.New("could not establish file watcher")
	}
	// TODO: establish a way to end the file watching and close the watcher
	// defer watcher.Close()

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("modified file:", event.Name)
					cfg.ReadConfigFileUsed()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	watcher.Add(cfg.configFileUsed)
	if err != nil {
		return fmt.Errorf("could not establish file watcher with file: %q", cfg.configFileUsed)
	}

	return nil
}

// BoolValue retrieves a bool by the key of the toggle and takes a default value if it does not exist
func BoolValue(toggleKey string, defaultValue bool) bool {
	return c.BoolValue(toggleKey, defaultValue)
}

func (cfg *config) BoolValue(toggleKey string, defaultValue bool) bool {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cBool, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(bool)
	if !ok {
		return defaultValue
	}

	return cBool
}

// StringValue retrieves a string by the key of the toggle and takes a default value if it does not exist
func StringValue(toggleKey string, defaultValue string) string {
	return c.StringValue(toggleKey, defaultValue)
}

func (cfg *config) StringValue(toggleKey string, defaultValue string) string {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cStr, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(string)
	if !ok {
		return defaultValue
	}

	return cStr
}

// Float64Value retrieves a float64 by the key of the toggle and takes a default value if it does not exist
func Float64Value(toggleKey string, defaultValue float64) float64 {
	return c.Float64Value(toggleKey, defaultValue)
}

func (cfg *config) Float64Value(toggleKey string, defaultValue float64) float64 {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cFloat64, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float64)
	if !ok {
		return defaultValue
	}

	return cFloat64
}

// Float32Value retrieves a float32 by the key of the toggle and takes a default value if it does not exist
// if the config value overflows the type requested, defaultValue will be returned
func Float32Value(toggleKey string, defaultValue float64) float64 {
	return c.Float64Value(toggleKey, defaultValue)
}

func (cfg *config) Float32Value(toggleKey string, defaultValue float32) float32 {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	cFloat32, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float32)
	if !ok {
		return defaultValue
	}

	return cFloat32
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }

func openLocalConfig(fileName string) (io.ReadCloser, error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0755)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not open file %q because it does not exist", fileName)
		}
		return nil, fmt.Errorf("could not open file %q: %w", fileName, err)
	}

	return file, nil
}
