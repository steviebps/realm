package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
	realmhttp "github.com/steviebps/realm/http"
	realm "github.com/steviebps/realm/pkg"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		flags := cmd.Flags()
		debug, _ := flags.GetBool("debug")
		logger := logging.Ctx(ctx)

		devMode, _ := flags.GetBool("dev")
		var realmConfig RealmConfig
		var err error
		if devMode {
			realmConfig = NewDefaultServerConfig()
		} else {
			var configPath string
			realmConfig, configPath, err = loadConfig(cmd)
			if err == nil && configPath == "" {
				err = fmt.Errorf("%w (or use --dev)", errNoConfig())
			}
			if err != nil {
				logger.ErrorCtx(ctx).Msg(err.Error())
				os.Exit(1)
			}
			logger.InfoCtx(ctx).Str("config", configPath).Msg("using config file")
		}

		serverConfig := realmConfig.Server

		if serverConfig.LogLevel != "" && !debug {
			level, err := zerolog.ParseLevel(serverConfig.LogLevel)
			if err != nil {
				logger.ErrorCtx(ctx).Msgf("invalid logLevel %q", serverConfig.LogLevel)
				os.Exit(1)
			}
			zerolog.SetGlobalLevel(level)
		}

		var shutdownDelay time.Duration
		if serverConfig.ShutdownDelay != "" {
			if shutdownDelay, err = time.ParseDuration(serverConfig.ShutdownDelay); err != nil || shutdownDelay < 0 {
				logger.ErrorCtx(ctx).Msgf("invalid shutdownDelay %q: want a duration such as \"5s\"", serverConfig.ShutdownDelay)
				os.Exit(1)
			}
		}

		portStr, err := flags.GetString("port")
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}

		if portStr == "" {
			portStr = serverConfig.Port
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}
		certFile := serverConfig.CertFile
		keyFile := serverConfig.KeyFile
		storageType := serverConfig.StorageType

		certFileEmpty := certFile == ""
		keyFileEmpty := keyFile == ""
		if certFileEmpty != keyFileEmpty {
			logger.ErrorCtx(ctx).Msg("certFile must be used in conjunction with keyFile")
			os.Exit(1)
		}

		logger.InfoCtx(ctx).Str("port", portStr).Str("certFile", certFile).Str("keyFile", keyFile).Str("storage", storageType).Bool("inheritable", serverConfig.Inheritable).Bool("debug", debug).Msg("server options")

		if len(serverConfig.StorageOptions) > 0 {
			logger.DebugCtx(ctx).Any("options", redactOptions(serverConfig.StorageOptions)).Msg("storage options")
		}

		stg, err := openStorage(serverConfig)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}
		defer stg.Close(ctx)

		if serverConfig.Inheritable {
			stg, err = storage.NewInheritableStorage(stg)
			if err != nil {
				logger.ErrorCtx(ctx).Msg(err.Error())
				os.Exit(1)
			}
		}

		// Create the root chamber unless it exists; servers starting together
		// against shared storage create it once.
		root, err := json.Marshal(&realm.Chamber{Rules: map[string]*realm.Flag{}})
		if err != nil {
			logger.ErrorCtx(ctx).Msgf("failed to marshal root entry during startup: %s", err.Error())
			os.Exit(1)
		}
		if err := storage.Update(ctx, stg, []string{"/"}, func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
			if cur["/"] != nil {
				return nil, nil
			}
			logger.DebugCtx(ctx).Msg("initializing root entry in storage")
			return []storage.Op{{Key: "/", Value: root}}, nil
		}); err != nil {
			logger.ErrorCtx(ctx).Msgf("failed to initialize root entry during startup: %s", err.Error())
			os.Exit(1)
		}

		if serverConfig.Auth == nil {
			logger.WarnCtx(ctx).Msg("AUTHENTICATION IS DISABLED: anyone who can reach this server can read and change every chamber. Add an \"auth\" block to the server config to require sign-in and API keys.")
		}

		draining := make(chan struct{})
		handler, err := realmhttp.NewHandler(ctx, realmhttp.HandlerConfig{Storage: stg, RequestTimeout: realmhttp.DefaultHandlerTimeout, Auth: serverConfig.Auth, Draining: draining})
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}

		// Requests run under baseCtx, which shutdown cancels: watch streams
		// never go idle on their own, so Shutdown would otherwise wait for them
		// until the process is killed.
		baseCtx, cancelBase := context.WithCancel(ctx)
		defer cancelBase()
		server := &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    64 << 10,
			BaseContext:       func(net.Listener) context.Context { return baseCtx },
		}
		server.RegisterOnShutdown(cancelBase)

		served := make(chan error, 1)
		go func() {
			logger.InfoCtx(ctx).Msg(fmt.Sprintf("Listening on port %s", portStr))
			if certFileEmpty {
				served <- server.ListenAndServe()
			} else {
				served <- server.ListenAndServeTLS(certFile, keyFile)
			}
		}()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		select {
		case err := <-served:
			logger.ErrorCtx(ctx).Msg(err.Error())
			_ = stg.Close(ctx)
			os.Exit(1)
		case sig := <-sigChan:
			logger.InfoCtx(ctx).Str("signal", sig.String()).Dur("delay", shutdownDelay).Msg("shutting down")
		}

		// Fail readiness first, and keep serving while load balancers notice.
		close(draining)
		if shutdownDelay > 0 {
			time.Sleep(shutdownDelay)
		}
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("server did not shut down cleanly")
		}
	},
}

// shutdownTimeout bounds how long in-flight requests may finish after a
// shutdown signal.
const shutdownTimeout = 20 * time.Second

func init() {
	serverCmd.Flags().String("port", "", "port to run server on")
	serverCmd.Flags().Bool("dev", false, "run server in dev mode")
	rootCmd.AddCommand(serverCmd)
}

// openStorage creates the storage backend named by the server config, without
// the inheritance wrapper.
func openStorage(c ServerConfig) (storage.Storage, error) {
	create, exists := storage.StorageOptions[c.StorageType]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", c.StorageType)
	}
	return create(c.StorageOptions)
}

// redactOptions returns storage options safe to log: credentials inside
// connection URLs (such as a Postgres url's password) are masked.
func redactOptions(options map[string]string) map[string]string {
	out := make(map[string]string, len(options))
	for k, v := range options {
		if u, err := url.Parse(v); err == nil && u.User != nil {
			v = u.Redacted()
		} else if strings.Contains(v, "password=") {
			// a keyword/value connection string
			v = "xxxxx"
		}
		out[k] = v
	}
	return out
}
