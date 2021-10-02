package rein

import (
	"errors"
	"fmt"
	"io"
	"path"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/steviebps/rein/utils"
	"golang.org/x/mod/semver"
)

type config struct {
	mu             sync.RWMutex
	rootChamber    *Chamber
	configPaths    []string
	defaultVersion string
	configFileUsed string
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

// AddConfigPath adds a file path to be look for the config when initializing
func AddConfigPath(path string) error { return c.AddConfigPath(path) }

func (cfg *config) AddConfigPath(filePath string) error {
	if filePath == "" {
		return errors.New("file path cannot be empty")
	}

	if path.Ext(filePath) != ".json" {
		return fmt.Errorf("%q does not have an acceptable file extension. please use JSON", filePath)
	}

	cfg.configPaths = append(c.configPaths, filePath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func ReadInConfig(watch bool) error { return c.ReadInConfig(watch) }

func (cfg *config) ReadInConfig(watch bool) error {
	var rc io.ReadCloser
	var err error
	var openedFileName string

	if len(cfg.configPaths) == 0 {
		return errors.New("could not open config because there were none specified. please add a config path")
	}

	for _, fileName := range cfg.configPaths {
		rc, err = utils.OpenLocalConfig(fileName)
		if err != nil {
			continue
		}
		defer rc.Close()
		openedFileName = fileName
		break
	}

	if rc == nil {
		return fmt.Errorf("could not open any of the config paths: %v", cfg.configPaths)
	}

	if watch {
		defer cfg.Watch()
	}

	return cfg.ReadChamber(rc, openedFileName)
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

func (cfg *config) ReadConfigFileUsed() error {
	cfg.mu.RLock()
	rc, err := utils.OpenLocalConfig(cfg.configFileUsed)
	if err != nil {
		return fmt.Errorf("error opening file %q: %w", cfg.configFileUsed, err)
	}
	defer rc.Close()

	cfg.mu.RUnlock()
	return cfg.ReadChamber(rc, cfg.configFileUsed)
}

func (cfg *config) Watch() {
	if cfg.configFileUsed == "" {
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
						err := cfg.ReadConfigFileUsed()
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

		watcher.Add(cfg.configFileUsed)
		init <- struct{}{}
		<-events
		fmt.Println("done watching file")
	}()
	<-init
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
