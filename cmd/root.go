package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/steviebps/realm/internal/logger"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

var home string
var cfgFile string
var globalChamber = realm.Chamber{Toggles: map[string]*realm.OverrideableToggle{}}
var realmCore realm.Realm

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
	if err := rootCmd.Execute(); err != nil {
		logger.ErrorString(fmt.Sprintf("Error while starting realm: %v", err))
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	var err error
	home, err = homedir.Dir()
	if err != nil {
		logger.ErrorString(err.Error())
		os.Exit(1)
	}
	rootCmd.Flags()
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "realm configuration file")
	rootCmd.PersistentFlags().String("app-version", "", "runs all commands with a specified version")
	realmCore = *realm.NewRealm(realm.RealmOptions{Logger: hclog.Default().Named("realm")})
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
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
		logger.ErrorString(err.Error())
		os.Exit(1)
	}
}

func retrieveRemoteConfig(url string) (*http.Response, error) {
	return http.Get(url)
}

// sets up the config for all sub-commands
func persistentPreRun(cmd *cobra.Command, args []string) {
	var jsonFile io.ReadCloser
	var err error
	chamberFile, _ := realmCore.StringValue("chamber", "")

	validURL, url := utils.IsURL(chamberFile)
	if validURL {
		res, err := retrieveRemoteConfig(url.String())

		if err != nil {
			logger.ErrorString(fmt.Sprintf("error trying to GET this resource %q: %v", chamberFile, err))
			os.Exit(1)
		}
		jsonFile = res.Body
	} else {
		jsonFile, err = utils.OpenLocalConfig(chamberFile)
		if err != nil {
			logger.ErrorString(fmt.Sprintf("error retrieving local config: %v", err))
			os.Exit(1)
		}
	}
	defer jsonFile.Close()

	if err := utils.ReadInterfaceWith(jsonFile, &globalChamber); err != nil {
		logger.ErrorString(fmt.Sprintf("error reading file %q: %v", chamberFile, err))
		os.Exit(1)
	}
}
