package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/steviebps/rein/internal/logger"
	"golang.org/x/mod/semver"
)

type config struct {
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
		return fmt.Errorf("%q is not an acceptable file extension. Please use JSON", filePath)
	}

	cfg.configPaths = append(c.configPaths, filePath)
	return nil
}

// ReadInConfig attempts to read in the first valid file from all of the config files added by AddConfigPath
func ReadInConfig() error { return c.ReadInConfig() }

func (cfg *config) ReadInConfig() error {
	var rc io.ReadCloser
	var err error

	for _, fileName := range cfg.configPaths {
		rc, err = retrieveLocalConfig(fileName)
		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error reading file: %v", err))
			continue
		}
		cfg.configFileUsed = fileName
		break
	}

	if rc == nil {
		return fmt.Errorf("error reading file %q", cfg.configFileUsed)
	}

	byteValue, err := io.ReadAll(rc)
	if err != nil {
		wrappedErr := fmt.Errorf("error reading file %q: %w", cfg.configFileUsed, err)
		logger.ErrorString(wrappedErr.Error())
		return wrappedErr
	}

	if err := json.Unmarshal(byteValue, &cfg.rootChamber); err != nil {
		wrappedErr := fmt.Errorf("error reading file %q: %w", cfg.configFileUsed, err)
		logger.ErrorString(wrappedErr.Error())
		return wrappedErr
	}

	return nil
}

// BoolValue retrieves a bool by the key of the toggle and takes a default value if it does not exist
func BoolValue(toggleKey string, defaultValue bool) bool {
	return c.BoolValue(toggleKey, defaultValue)
}

func (cfg *config) BoolValue(toggleKey string, defaultValue bool) bool {
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
	cFloat32, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float32)
	if !ok {
		return defaultValue
	}

	return cFloat32
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }

func retrieveLocalConfig(fileName string) (io.ReadCloser, error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0755)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not open file %q because it does not exist", fileName)
		}
		return nil, fmt.Errorf("could not open file %q: %w", fileName, err)
	}

	return file, nil
}
