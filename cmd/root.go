package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	rein "github.com/steviebps/rein/pkg"
	utils "github.com/steviebps/rein/utils"
)

type selectAction func(*rein.Chamber)

type openOption struct {
	Name       string
	Associated *rein.Chamber
	Action     selectAction
}

func (option openOption) Run() {
	option.Action(option.Associated)
}

var cfgFile string
var chamber string
var c = rein.Chamber{Toggles: []*rein.Toggle{}, Children: []*rein.Chamber{}}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "rein",
	Short: "Local and remote configuration management",
	Long:  `CLI tool that helps with configuration management for local and remote JSON files`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		chamberFile := viper.GetString("chamber")

		if !utils.Exists(chamberFile) {
			fmt.Printf("Could not find chamber file: \"%s\"\n", chamberFile)
			os.Exit(1)
		}

		jsonFile, err := os.Open(chamberFile)
		if err != nil {
			fmt.Printf("Could not open chamber file: %s\n", chamberFile)
			os.Exit(1)
		}
		defer jsonFile.Close()

		byteValue, err := ioutil.ReadAll(jsonFile)
		if err != nil {
			fmt.Printf("Error reading chamber file: %s\n", err)
			os.Exit(1)
		}

		if err := json.Unmarshal(byteValue, &c); err != nil {
			fmt.Printf("Error reading JSON: %s\n", err)
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

	home, err := homedir.Dir()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	defaultConfigPath := filepath.Join(home, "/.rein.yaml")

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", defaultConfigPath, "Rein config file")
	rootCmd.PersistentFlags().StringVar(&chamber, "chamber", "", "The file to read chambers from")
	viper.BindPFlag("chamber", rootCmd.PersistentFlags().Lookup("chamber"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" && utils.Exists(cfgFile) {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Search config in home directory with name ".rein" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigName(".rein")
	}

	viper.AutomaticEnv() // read in environment variables that match
	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		//fmt.Println("Using rein config file:", viper.ConfigFileUsed())
	}
}
