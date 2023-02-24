package cmd

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"

	realm "github.com/steviebps/realm/pkg"
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

func init() {
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().String("config", "", "realm configuration file")
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	logger := hclog.Default().Named("realm")
	if err := rootCmd.Execute(); err != nil {
		logger.Error(fmt.Sprintf("Error while starting realm: %v", err))
		os.Exit(1)
	}
}

// func retrieveRemoteConfig(url string) (*http.Response, error) {
// 	return http.Get(url)
// }

// sets up the config for all sub-commands
func persistentPreRun(cmd *cobra.Command, args []string) {
	flags := cmd.Flags()
	cfgFile, _ := flags.GetString("config")
	logger := hclog.Default().Named("realm")
	logger.SetLevel(hclog.NoLevel)

	_, err := parseConfig[RealmConfig](cfgFile)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// home, err := homedir.Dir()
	// if err != nil {
	// 	logger.Error(err.Error())
	// 	os.Exit(1)
	// }

	// if cfgFile != "" {
	// 	// Use config file from the flag.
	// 	realmCore.SetConfigFile(cfgFile)
	// } else {
	// 	realmCore.AddConfigPath("./")
	// 	realmCore.AddConfigPath(home + "/.realm/")
	// 	realmCore.SetConfigName("realm.json")
	// }

	// // If a config file is found, read it in.
	// if err := realmCore.ReadInConfig(false); err != nil {
	// 	realmCore.Logger().Error(err.Error())
	// 	os.Exit(1)
	// }

	// chamberFile, _ := realmCore.StringValue("chamber", "")
	// validURL, url := utils.IsURL(chamberFile)
	// var jsonFile io.ReadCloser
	// defer jsonFile.Close()
	// if validURL {
	// 	res, err := retrieveRemoteConfig(url.String())

	// 	if err != nil {
	// 		realmCore.Logger().Error(fmt.Sprintf("error trying to GET this resource %q: %v", chamberFile, err))
	// 		os.Exit(1)
	// 	}
	// 	jsonFile = res.Body
	// } else {
	// 	var err error
	// 	if jsonFile, err = utils.OpenFile(chamberFile); err != nil {
	// 		realmCore.Logger().Error(fmt.Sprintf("error retrieving local config: %v", err))
	// 		os.Exit(1)
	// 	}
	// }

	// if err := utils.ReadInterfaceWith(jsonFile, &globalChamber); err != nil {
	// 	realmCore.Logger().Error(fmt.Sprintf("error reading file %q: %v", chamberFile, err))
	// 	os.Exit(1)
	// }
}
