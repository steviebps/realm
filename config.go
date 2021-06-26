package rein

import (
	"fmt"
	"path"

	"golang.org/x/mod/semver"
)

type Config struct {
	rootChamber    Chamber
	configPaths    []string
	defaultVersion string
}

var config *Config

func init() {
	config = New()
}

func New() *Config {
	return &Config{}
}

func SetDefaultVersion(version string) error {
	if isValidVersion := semver.IsValid(version); !isValidVersion {
		return fmt.Errorf("%q is not a valid semantic version", version)
	}

	config.defaultVersion = version
	return nil
}

func AddConfigPath(path string) error { return config.AddConfigPath(path) }

func (cfg *Config) AddConfigPath(filePath string) error {
	if path.Ext(filePath) != "json" {
		return fmt.Errorf("%q is not an acceptable file extension. Please use JSON.", filePath)
	}

	config.configPaths = append(config.configPaths, filePath)
	return nil
}
