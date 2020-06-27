/*
Copyright © 2020 NAME HERE <EMAIL ADDRESS>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	utils "github.com/steviebps/rein/pkg/utils"
)

var cfgFile string
var chamber string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "rein",
	Short: "Local and remote configuration management",
	Long:  `CLI tool that helps with configuration management for local and remote JSON files`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "$HOME/.rein.yaml", "config file")
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
