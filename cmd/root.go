package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
	realmtrace "github.com/steviebps/realm/trace"
)

// Version the version of realm
var Version = "development"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "realm",
	Short:             "Local and remote configuration management",
	Long:              `CLI for managing application configuration of local and remote JSON files`,
	PersistentPreRunE: persistentPreRunE,
	DisableAutoGenTag: true,
	Version:           Version,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
}

var shutdownFn func(ctx context.Context) error

func init() {
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().StringP("config", "c", "", "realm configuration file")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "run realm in debug mode")
	rootCmd.PersistentFlags().Bool("stdouttraces", false, "use stdout for trace exporter")
	rootCmd.PersistentFlags().Bool("notraces", false, "disable tracing")
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	logger := logging.NewTracedLogger()
	ctx := logger.WithContext(context.Background())

	err := rootCmd.ExecuteContext(ctx)

	if shutdownFn != nil {
		if shutdownErr := shutdownFn(ctx); shutdownErr != nil {
			fmt.Printf("failed to shutdown TracerProvider: %s\n", shutdownErr)
		}
	}

	if err != nil {
		os.Exit(1)
	}
}

// sets up the config for all sub-commands
func persistentPreRunE(cmd *cobra.Command, args []string) error {
	var err error
	ctx := cmd.Context()
	logger := logging.Ctx(ctx)
	flags := cmd.Flags()
	debug, _ := flags.GetBool("debug")
	stdoutTraces, _ := flags.GetBool("stdouttraces")
	noTraces, _ := flags.GetBool("notraces")

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	devMode, _ := flags.GetBool("dev")
	if !devMode && !noTraces {
		shutdownFn, err = realmtrace.SetupOtelInstrumentation(ctx, stdoutTraces)
		if err != nil {
			return err
		}
	}
	logger.DebugCtx(ctx).Msgf("realm version: %s", Version)

	return nil
}
