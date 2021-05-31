package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"github.com/steviebps/rein/internal/logger"
	rein "github.com/steviebps/rein/pkg"
	utils "github.com/steviebps/rein/utils"
)

var home string
var cfgFile string
var chamber string
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
	viper.BindPFlag("app-version", rootCmd.PersistentFlags().Lookup("app-version"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath(".")
		viper.AddConfigPath(home + "/.rein")
		viper.SetConfigName("rein")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {

		if configFileUsed := viper.ConfigFileUsed(); configFileUsed != "" {
			logger.ErrorString(fmt.Sprintf("Error reading config file: %v", configFileUsed))
		} else {
			logger.ErrorString(err.Error())
		}

		os.Exit(1)
	}
}

func retrieveRemoteConfig(url string) (*http.Response, error) {
	res, err := http.Get(url)

	if err != nil {
		return nil, err
	}

	return res, nil
}

func retrieveLocalConfig(fileName string) (io.ReadCloser, error) {
	if !utils.Exists(fileName) {
		return nil, fmt.Errorf("Could not find file %q", fileName)
	}

	file, err := os.Open(fileName)
	if err != nil {
		return nil, fmt.Errorf("Could not open file %q: %v", fileName, err)
	}

	return file, nil
}

// sets up the config for all sub-commands
func configPreRun(cmd *cobra.Command, args []string) {
	var jsonFile io.ReadCloser
	var err error
	chamberFile := viper.GetString("chamber")

	validURL, url := utils.IsURL(chamberFile)
	if validURL {
		res, err := retrieveRemoteConfig(url.String())

		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error trying to GET this resource %q: %v", chamberFile, err))
			os.Exit(1)
		}
		jsonFile = res.Body
	} else {
		jsonFile, err = retrieveLocalConfig(chamberFile)
		if err != nil {
			logger.ErrorString(fmt.Sprintf("Error retrieving local config: %v", err))
			os.Exit(1)
		}
	}
	defer jsonFile.Close()

	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		logger.ErrorString(fmt.Sprintf("Error reading file %q: %v", chamberFile, err))
		os.Exit(1)
	}

	if err := json.Unmarshal(byteValue, &globalChamber); err != nil {
		logger.ErrorString(fmt.Sprintf("Error reading %q: %v", chamberFile, err))
		os.Exit(1)
	}
}
