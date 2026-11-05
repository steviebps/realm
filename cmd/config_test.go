package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// isolateConfig points the default config locations at empty temporary
// directories and clears the realm environment variables, returning the
// user-level and system-level default config paths.
func isolateConfig(t *testing.T) (userPath, systemPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	for _, name := range []string{ConfigEnv, AddressEnv, TokenEnv} {
		t.Setenv(name, "")
	}
	old := systemConfigDir
	systemConfigDir = filepath.Join(t.TempDir(), "etc", "realm")
	t.Cleanup(func() { systemConfigDir = old })

	paths := DefaultConfigPaths()
	userPath = paths[0]
	if len(paths) > 1 {
		systemPath = paths[1]
	}
	return userPath, systemPath
}

// testCmd returns a command with the global flags realm's commands read,
// parsed from args. rootCmd is not used because cobra keeps flag values
// between executions.
func testCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("address", "", "")
	cmd.Flags().String("token", "", "")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatal(err)
	}
	cmd.SetContext(context.Background())
	return cmd
}

func writeConfig(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindConfig(t *testing.T) {
	tests := []struct {
		name string
		// setup creates files and sets variables, returning the --config
		// flag value (or "") and the expected path.
		setup func(t *testing.T, userPath, systemPath string) (flag, want string)
	}{
		{"nothing", func(t *testing.T, _, _ string) (string, string) {
			return "", ""
		}},
		{"flag beats variable and defaults", func(t *testing.T, userPath, _ string) (string, string) {
			writeConfig(t, userPath, `{}`)
			t.Setenv(ConfigEnv, "/from/env.json")
			return "/from/flag.json", "/from/flag.json"
		}},
		{"variable beats defaults", func(t *testing.T, userPath, _ string) (string, string) {
			writeConfig(t, userPath, `{}`)
			t.Setenv(ConfigEnv, "/from/env.json")
			return "", "/from/env.json"
		}},
		{"user default beats system default", func(t *testing.T, userPath, systemPath string) (string, string) {
			writeConfig(t, userPath, `{}`)
			if systemPath != "" {
				writeConfig(t, systemPath, `{}`)
			}
			return "", userPath
		}},
		{"system default", func(t *testing.T, _, systemPath string) (string, string) {
			if systemPath == "" {
				t.Skip("no system config directory on this platform")
			}
			writeConfig(t, systemPath, `{}`)
			return "", systemPath
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userPath, systemPath := isolateConfig(t)
			flag, want := tt.setup(t, userPath, systemPath)
			var args []string
			if flag != "" {
				args = []string{"--config", flag}
			}
			got, err := findConfig(testCmd(t, args...))
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("findConfig = %q, want %q", got, want)
			}
		})
	}
}

// TestLoadConfigNamedFileMustExist verifies that a missing file named by
// $REALM_CONFIG is an error rather than a silent fall back to the defaults.
func TestLoadConfigNamedFileMustExist(t *testing.T) {
	userPath, _ := isolateConfig(t)
	writeConfig(t, userPath, `{"client":{"address":"http://default"}}`)
	t.Setenv(ConfigEnv, filepath.Join(t.TempDir(), "missing.json"))

	if _, _, err := loadConfig(testCmd(t)); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("loadConfig with a missing $%s: err = %v", ConfigEnv, err)
	}
}

func TestNewClientAddress(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		env    string
		config string // client.address in the default config file
		want   string
	}{
		{name: "flag", flag: "http://flag", env: "http://env", config: "http://config", want: "flag"},
		{name: "variable", env: "http://env", config: "http://config", want: "env"},
		{name: "default config file", config: "http://config", want: "config"},
		{name: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userPath, _ := isolateConfig(t)
			if tt.config != "" {
				writeConfig(t, userPath, `{"client":{"address":"`+tt.config+`"}}`)
			}
			t.Setenv(AddressEnv, tt.env)
			var args []string
			if tt.flag != "" {
				args = []string{"--address", tt.flag}
			}

			c, err := newClient(testCmd(t, args...))
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), AddressEnv) {
					t.Fatalf("newClient without an address: err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			req, err := c.NewRequest(context.Background(), "GET", "/v1/chambers/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.Host != tt.want {
				t.Fatalf("request host = %q, want %q", req.URL.Host, tt.want)
			}
		})
	}
}
