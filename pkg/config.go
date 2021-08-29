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

type Config struct {
	rootChamber    Chamber
	configPaths    []string
	defaultVersion string
	configFileUsed string
}

var config *Config

func init() {
	config = newConfig()
}

func newConfig() *Config {
	return &Config{}
}

func SetVersion(version string) error { return config.SetVersion(version) }

func (cfg *Config) SetVersion(version string) error {
	if isValidVersion := semver.IsValid(version); !isValidVersion {
		return fmt.Errorf("%q is not a valid semantic version", version)
	}

	cfg.defaultVersion = version
	return nil
}

func AddConfigPath(path string) error { return config.AddConfigPath(path) }

func (cfg *Config) AddConfigPath(filePath string) error {
	if filePath == "" {
		return errors.New("Config path cannot be empty")
	}

	if path.Ext(filePath) != ".json" {
		return fmt.Errorf("%q is not an acceptable file extension. Please use JSON", filePath)
	}

	cfg.configPaths = append(config.configPaths, filePath)
	return nil
}

func ReadInConfig() error { return config.ReadInConfig() }

func (cfg *Config) ReadInConfig() error {
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

func BoolValue(toggleKey string, defaultValue bool) bool {
	return config.BoolValue(toggleKey, defaultValue)
}

func (cfg *Config) BoolValue(toggleKey string, defaultValue bool) bool {
	cBool, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(bool)
	if !ok {
		return defaultValue
	}

	return cBool
}

func StringValue(toggleKey string, defaultValue string) string {
	return config.StringValue(toggleKey, defaultValue)
}

func (cfg *Config) StringValue(toggleKey string, defaultValue string) string {
	cStr, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(string)
	if !ok {
		return defaultValue
	}

	return cStr
}

// Float64Value retrieves a float64 by the key of the toggle and takes a default value if it does not exist
func Float64Value(toggleKey string, defaultValue float64) float64 {
	return config.Float64Value(toggleKey, defaultValue)
}

func (cfg *Config) Float64Value(toggleKey string, defaultValue float64) float64 {
	cFloat64, ok := cfg.rootChamber.GetToggleValue(toggleKey, cfg.defaultVersion).(float64)
	if !ok {
		return defaultValue
	}

	return cFloat64
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }

func retrieveLocalConfig(fileName string) (io.ReadCloser, error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0755)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("could not open file %q: %w", fileName, err)
	}

	return file, nil
}
