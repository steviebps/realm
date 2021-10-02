package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/steviebps/rein/internal/logger"
	rein "github.com/steviebps/rein/pkg"
	utils "github.com/steviebps/rein/utils"
)

var home string
var cfgFile string
var globalChamber = rein.Chamber{Toggles: map[string]*rein.Toggle{}, Children: []*rein.Chamber{}}

// Version the version of rein
var Version = "development"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "rein",
	Short:             "Local and remote configuration management",
	Long:              `CLI for managing application configuration of local and remote JSON files`,
	PersistentPreRun:  configPreRun,
	DisableAutoGenTag: true,
	Version:           Version,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		logger.ErrorString(fmt.Sprintf("Error while starting rein: %v", err))
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

	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "rein configuration file")
	rootCmd.PersistentFlags().String("app-version", "", "runs all commands with a specified version")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		rein.SetConfigFile(cfgFile)
	} else {
		rein.AddConfigPath("./")
		rein.AddConfigPath(home + "/.rein/")
		rein.SetConfigName("rein.json")
	}

	// If a config file is found, read it in.
	if err := rein.ReadInConfig(false); err != nil {
		logger.ErrorString(err.Error())
		os.Exit(1)
	}
}

func retrieveRemoteConfig(url string) (*http.Response, error) {
	return http.Get(url)
}

// sets up the config for all sub-commands
func configPreRun(cmd *cobra.Command, args []string) {
	var jsonFile io.ReadCloser
	var err error
	chamberFile := rein.StringValue("chamber", "")

	validURL, url := utils.IsURL(chamberFile)
	if validURL {
		res, err := retrieveRemoteConfig(url.String())

		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error trying to GET this resource %q: %v", chamberFile, err))
			os.Exit(1)
		}
		jsonFile = res.Body
	} else {
		jsonFile, err = utils.OpenLocalConfig(chamberFile)
		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error retrieving local config: %v", err))
			os.Exit(1)
		}
	}
	defer jsonFile.Close()

	if err := utils.ReadInterfaceWith(jsonFile, &globalChamber); err != nil {
		logger.ErrorString(fmt.Sprintf("Error reading file %q: %v", chamberFile, err))
		os.Exit(1)
	}
}
