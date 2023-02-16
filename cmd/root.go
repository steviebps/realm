package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

var globalChamber = realm.Chamber{Toggles: map[string]*realm.OverrideableToggle{}}

// Version the version of realm
var Version = "development"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "realm",
	Short:             "Local and remote configuration management",
	Long:              `CLI for managing application configuration of local and remote JSON files`,
	PersistentPreRun:  persistentPreRun,
	DisableAutoGenTag: true,
	Version:           Version,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	logger := hclog.Default().Named("realm")
	logger.SetLevel(hclog.Trace)
	realmCore := realm.NewRealm(realm.RealmOptions{Logger: logger})
	ctx := context.WithValue(context.Background(), "core", realmCore)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		realmCore.Logger().Error(fmt.Sprintf("Error while starting realm: %v", err))
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().String("config", "", "realm configuration file")
	rootCmd.PersistentFlags().String("app-version", "", "runs all commands with a specified version")
}

func retrieveRemoteConfig(url string) (*http.Response, error) {
	return http.Get(url)
}

// sets up the config for all sub-commands
func persistentPreRun(cmd *cobra.Command, args []string) {
	realmCore := cmd.Context().Value("core").(*realm.Realm)
	flags := cmd.Flags()
	cfgFile, _ := flags.GetString("config")

	home, err := homedir.Dir()
	if err != nil {
		realmCore.Logger().Error(err.Error())
		os.Exit(1)
	}

	if cfgFile != "" {
		// Use config file from the flag.
		realmCore.SetConfigFile(cfgFile)
	} else {
		realmCore.AddConfigPath("./")
		realmCore.AddConfigPath(home + "/.realm/")
		realmCore.SetConfigName("realm.json")
	}

	// If a config file is found, read it in.
	if err := realmCore.ReadInConfig(false); err != nil {
		realmCore.Logger().Error(err.Error())
		os.Exit(1)
	}

	chamberFile, _ := realmCore.StringValue("chamber", "")
	validURL, url := utils.IsURL(chamberFile)
	var jsonFile io.ReadCloser
	if validURL {
		res, err := retrieveRemoteConfig(url.String())

		if err != nil {
			realmCore.Logger().Error(fmt.Sprintf("error trying to GET this resource %q: %v", chamberFile, err))
			os.Exit(1)
		}
		jsonFile = res.Body
	} else {
		var err error
		if jsonFile, err = utils.OpenLocalConfig(chamberFile); err != nil {
			realmCore.Logger().Error(fmt.Sprintf("error retrieving local config: %v", err))
			os.Exit(1)
		}
	}
	defer jsonFile.Close()

	if err := utils.ReadInterfaceWith(jsonFile, &globalChamber); err != nil {
		realmCore.Logger().Error(fmt.Sprintf("error reading file %q: %v", chamberFile, err))
		os.Exit(1)
	}
}
