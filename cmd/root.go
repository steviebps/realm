package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	realmtrace "github.com/steviebps/realm/trace"
)

// Version the version of realm
var Version = "development"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "realm",
	Short:             "Local and remote configuration management",
	Long:              `CLI for managing application configuration of local and remote JSON files`,
	PersistentPreRun:  persistentPreRun,
	PersistentPostRun: persistentPostRun,
	DisableAutoGenTag: true,
	Version:           Version,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
}

var shutdownFn func(ctx context.Context) error

func init() {
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().StringP("config", "c", "", "realm configuration file")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "run realm in debug mode")
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("Error while starting realm: %v\n", err)
		os.Exit(1)
	}
}

// sets up the config for all sub-commands
func persistentPreRun(cmd *cobra.Command, args []string) {
	var err error
	ctx := cmd.Context()
	flags := cmd.Flags()
	debug, _ := flags.GetBool("debug")

	level := hclog.Info
	if debug {
		level = hclog.Debug
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:                 "realm",
		Level:                level,
		Output:               cmd.OutOrStderr(),
		TimeFn:               time.Now,
		ColorHeaderAndFields: true,
		Color:                hclog.AutoColor,
	})

	hclog.SetDefault(logger)

	shutdownFn, err = realmtrace.SetupOtelInstrumentation(ctx, false)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func persistentPostRun(cmd *cobra.Command, args []string) {
	if err := shutdownFn(context.Background()); err != nil {
		fmt.Printf("failed to shutdown TracerProvider: %s\n", err)
	}
}
