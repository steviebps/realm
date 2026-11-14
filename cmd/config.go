package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/utils"
)

// ConfigEnv names the environment variable that holds the path of the config
// file when --config is not given.
const ConfigEnv = "REALM_CONFIG"

// ConfigFileName is the name of the config file in realm's default config
// directories.
const ConfigFileName = "realm.json"

// systemConfigDir is the system-wide config directory searched on Unix-like
// systems; the container image mounts its config here. A variable for tests.
var systemConfigDir = "/etc/realm"

type RealmConfig struct {
	Client ClientConfig `json:"client,omitempty"`
	Server ServerConfig `json:"server,omitempty"`
}

// NewDefaultServerConfig returns the config of a --dev server: in-memory
// storage whose entries never expire (bigcache without a life_window), so
// dev data lasts until the server stops.
func NewDefaultServerConfig() RealmConfig {
	return RealmConfig{
		Client: ClientConfig{},
		Server: ServerConfig{StorageType: "bigcache", StorageOptions: map[string]string{}, Port: "8080", Inheritable: true},
	}
}

type ServerConfig struct {
	StorageType    string            `json:"storage"`
	StorageOptions map[string]string `json:"options"`
	Port           string            `json:"port"`
	CertFile       string            `json:"certFile"`
	KeyFile        string            `json:"keyFile"`
	// LogLevel is the minimum level logged: debug, info, warn, or error.
	LogLevel    string `json:"logLevel"`
	Inheritable bool   `json:"inheritable"`
	// ShutdownDelay is how long the server keeps serving after a shutdown
	// signal while GET /readyz reports 503, so load balancers stop sending it
	// traffic first (a duration such as "5s"; default none).
	ShutdownDelay string `json:"shutdownDelay,omitempty"`
	// Auth enables authentication; without it the server is open to anyone
	// who can reach it.
	Auth *auth.Config `json:"auth,omitempty"`
	// MaxStreams caps the watch (SSE) streams the server holds open at once
	// (default 10,000). Evaluation streams, which anyone holding a published
	// evaluate key can open, may take at most three quarters of them.
	MaxStreams int `json:"maxStreams,omitempty"`
	// MaxSubtreeChambers caps the chambers below the root of a subtree read
	// or watch (default 10,000); larger subtrees are refused.
	MaxSubtreeChambers int `json:"maxSubtreeChambers,omitempty"`
}

type ClientConfig struct {
	Address string `json:"address"`
	// Token is an API key; it may reference an environment variable as ${NAME}.
	Token string `json:"token,omitempty"`
}

// DefaultConfigPaths returns, in order, where realm looks for a config file
// when neither --config nor $REALM_CONFIG names one: realm/realm.json in the
// user's config directory (os.UserConfigDir: $XDG_CONFIG_HOME or ~/.config on
// Linux, ~/Library/Application Support on macOS, %AppData% on Windows), then
// /etc/realm/realm.json on Unix-like systems.
//
// The working directory is deliberately not searched: a realm.json in a
// checked-out repository could otherwise point the CLI, and $REALM_TOKEN, at
// another server.
func DefaultConfigPaths() []string {
	var paths []string
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "realm", ConfigFileName))
	}
	if runtime.GOOS != "windows" && systemConfigDir != "" {
		paths = append(paths, filepath.Join(systemConfigDir, ConfigFileName))
	}
	return paths
}

// findConfig returns the path of the config file to use: --config, then
// $REALM_CONFIG, then the first of DefaultConfigPaths that exists. It returns
// "" when none is named and no default file exists. A file named by the flag
// or the variable is returned even if it does not exist, so reading it fails
// loudly instead of silently falling back.
func findConfig(cmd *cobra.Command) (string, error) {
	if path, _ := cmd.Flags().GetString("config"); path != "" {
		return path, nil
	}
	if path := os.Getenv(ConfigEnv); path != "" {
		return path, nil
	}
	for _, path := range DefaultConfigPaths() {
		_, err := os.Stat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("could not read config file %q: %w", path, err)
		}
	}
	return "", nil
}

// loadConfig finds (see findConfig) and parses the config file. It returns the
// path it read, or "" with an empty config when there is no config file.
func loadConfig(cmd *cobra.Command) (RealmConfig, string, error) {
	path, err := findConfig(cmd)
	if err != nil || path == "" {
		return RealmConfig{}, "", err
	}
	config, err := parseConfig(path)
	return config, path, err
}

// errNoConfig explains where a config file can come from, for commands that
// require one.
func errNoConfig() error {
	where := "pass --config or set $" + ConfigEnv
	if paths := DefaultConfigPaths(); len(paths) > 0 {
		where += ", or create " + strings.Join(paths, " or ")
	}
	return errors.New("no config file: " + where)
}

func parseConfig(path string) (RealmConfig, error) {
	var config RealmConfig

	file, err := utils.OpenFile(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	err = utils.ReadInterfaceWith(file, &config)
	return config, err
}
