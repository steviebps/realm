package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	addTracesFlags(rootCmd.PersistentFlags())
}

// TracesEnv names the environment variable that chooses the telemetry
// exporter when --traces is not given.
const TracesEnv = "REALM_TRACES"

// Telemetry exporters accepted by --traces and $REALM_TRACES.
const (
	tracesOff    = "off"
	tracesOTLP   = "otlp"
	tracesStdout = "stdout"
)

// addTracesFlags registers --traces and the deprecated flags it replaces.
func addTracesFlags(flags *pflag.FlagSet) {
	flags.String("traces", tracesOff, "export traces and metrics: off, otlp, or stdout (or set $"+TracesEnv+")")
	// Kept so existing invocations keep working; --traces replaces both.
	flags.Bool("stdouttraces", false, "use stdout for trace exporter")
	flags.Bool("notraces", false, "disable tracing")
	_ = flags.MarkDeprecated("stdouttraces", "use --traces=stdout")
	_ = flags.MarkDeprecated("notraces", "tracing is off by default")
}

// tracesMode returns the telemetry exporter to use: --traces, then the
// deprecated --notraces / --stdouttraces, then $REALM_TRACES, else off.
// Telemetry is opt-in so that commands never try to reach a collector nobody
// configured.
func tracesMode(flags *pflag.FlagSet) (string, error) {
	mode := tracesOff
	switch {
	case flags.Changed("traces"):
		mode, _ = flags.GetString("traces")
	case flags.Changed("notraces"):
		// Off already; --notraces only ever turned telemetry off.
	case flags.Changed("stdouttraces"):
		if stdout, _ := flags.GetBool("stdouttraces"); stdout {
			mode = tracesStdout
		}
	case os.Getenv(TracesEnv) != "":
		mode = os.Getenv(TracesEnv)
	}
	switch mode = strings.ToLower(strings.TrimSpace(mode)); mode {
	case tracesOff, tracesOTLP, tracesStdout:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid traces value %q: want %s, %s, or %s", mode, tracesOff, tracesOTLP, tracesStdout)
	}
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

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	traces, err := tracesMode(flags)
	if err != nil {
		return err
	}
	if traces != tracesOff {
		shutdownFn, err = realmtrace.SetupOtelInstrumentation(ctx, traces == tracesStdout, Version)
		if err != nil {
			return err
		}
	}
	logger.DebugCtx(ctx).Msgf("realm version: %s", Version)

	return nil
}
