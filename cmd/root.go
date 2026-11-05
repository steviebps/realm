package cmd

import (
	"context"
	"os"
	"strings"
	"time"

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

// telemetryFlushTimeout bounds how long exporting buffered telemetry may delay
// the process's exit.
const telemetryFlushTimeout = 5 * time.Second

func init() {
	rootCmd.SetVersionTemplate(`{{printf "%s\n" .Version}}`)
	rootCmd.PersistentFlags().StringP("config", "c", "", "realm configuration file (or set $"+ConfigEnv+"; default: "+strings.Join(DefaultConfigPaths(), ", ")+")")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "run realm in debug mode")
	rootCmd.PersistentFlags().Bool("stdouttraces", false, "use stdout for trace exporter")
	rootCmd.PersistentFlags().Bool("notraces", false, "disable tracing")
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	logger := logging.NewTracedLogger()
	ctx := logger.WithContext(context.Background())

	err := rootCmd.ExecuteContext(ctx)

	if shutdownFn != nil {
		// Bounded so an unreachable collector cannot hold up the exit.
		flushCtx, cancel := context.WithTimeout(ctx, telemetryFlushTimeout)
		defer cancel()
		if shutdownErr := shutdownFn(flushCtx); shutdownErr != nil {
			logger.ErrorCtx(ctx).Str("error", shutdownErr.Error()).Msg("could not flush telemetry")
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

	// The server exports telemetry unless told not to; other commands only
	// when a collector is configured, so CLI runs do not log export failures.
	devMode, _ := flags.GetBool("dev")
	wanted := cmd == serverCmd || stdoutTraces || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
	if wanted && !devMode && !noTraces {
		shutdownFn, err = realmtrace.SetupOtelInstrumentation(ctx, stdoutTraces, Version)
		if err != nil {
			return err
		}
	}
	logger.DebugCtx(ctx).Msgf("realm version: %s", Version)

	return nil
}
