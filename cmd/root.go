package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	rein "github.com/steviebps/rein/pkg"
	utils "github.com/steviebps/rein/utils"
)

var home string
var cfgFile string
var chamber string
var globalChamber = rein.Chamber{Toggles: map[string]*rein.Toggle{}, Children: []*rein.Chamber{}}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "rein",
	Short:             "Local and remote configuration management",
	Long:              `CLI for managing application configuration of local and remote JSON files`,
	DisableAutoGenTag: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		var jsonFile io.ReadCloser
		var err error
		chamberFile := viper.GetString("chamber")

		validURL, url := utils.IsURL(chamberFile)
		if validURL {
			res, err := http.Get(url.String())

			if err != nil {
				fmt.Printf("Error trying to GET this resource \"%v\": %v\n", chamberFile, err)
				log.Fatal(err)
			}
			jsonFile = res.Body
			defer jsonFile.Close()
		} else {
			if !utils.Exists(chamberFile) {
				fmt.Printf("Could not find file \"%v\"\n", chamberFile)
				os.Exit(1)
			}

			jsonFile, err = os.Open(chamberFile)
			if err != nil {
				fmt.Printf("Could not open file \"%v\": %v\n", chamberFile, err)
				os.Exit(1)
			}
			defer jsonFile.Close()
		}

		byteValue, err := ioutil.ReadAll(jsonFile)
		if err != nil {
			fmt.Printf("Error reading file \"%v\": %v\n", chamberFile, err)
			os.Exit(1)
		}

		if err := json.Unmarshal(byteValue, &globalChamber); err != nil {
			fmt.Printf("Error reading \"%v\": %v\n", chamberFile, err)
			os.Exit(1)
		}
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error while starting rein: ", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	var err error
	home, err = homedir.Dir()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	defaultConfigPath := filepath.Join(home, "/.rein/rein.yaml")
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", defaultConfigPath, "rein configuration file")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" && utils.Exists(cfgFile) {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {

		// Search config in home directory with name ".rein" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigName("rein")
	}

	viper.AutomaticEnv() // read in environment variables that match
	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Error reading config file: %v\n", viper.ConfigFileUsed())
	}
}
