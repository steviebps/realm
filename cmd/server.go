package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

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
	PreRun: func(cmd *cobra.Command, args []string) {
		devMode, _ := cmd.Flags().GetBool("dev")
		if !devMode {
			_ = cmd.MarkFlagRequired("config")
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		flags := cmd.Flags()
		debug, _ := flags.GetBool("debug")
		logger := logging.Ctx(ctx)

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}

		devMode, _ := flags.GetBool("dev")
		if !devMode && configPath == "" {
			logger.ErrorCtx(ctx).Msg("config must be specified")
			os.Exit(1)
		}
		var realmConfig RealmConfig

		if devMode {
			realmConfig = NewDefaultServerConfig()
		} else {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				logger.ErrorCtx(ctx).Msg(err.Error())
				os.Exit(1)
			}
		}

		serverConfig := realmConfig.Server

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
			logger.DebugCtx(ctx).Any("options", serverConfig.StorageOptions).Msg("storage options")
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

		var notFoundErr *storage.NotFoundError
		if _, err = stg.Get(ctx, "/"); errors.As(err, &notFoundErr) {
			logger.DebugCtx(ctx).Msg("initializing root entry in storage")

			b, err := json.Marshal(&realm.Chamber{Rules: map[string]*realm.Flag{}})
			if err != nil {
				logger.ErrorCtx(ctx).Msgf("failed to marshal root entry during startup: %s", err.Error())
				os.Exit(1)
			}

			entry := storage.StorageEntry{Key: "/", Value: b}
			if err := stg.Put(ctx, entry); err != nil {
				logger.ErrorCtx(ctx).Msgf("failed to initialize root entry during startup: %s", err.Error())
				os.Exit(1)
			}
		}

		if serverConfig.Auth == nil {
			logger.WarnCtx(ctx).Msg("AUTHENTICATION IS DISABLED: anyone who can reach this server can read and change every chamber. Add an \"auth\" block to the server config to require sign-in and API keys.")
		}

		handler, err := realmhttp.NewHandler(ctx, realmhttp.HandlerConfig{Storage: stg, RequestTimeout: realmhttp.DefaultHandlerTimeout, Auth: serverConfig.Auth})
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}

		server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: handler}

		go func() {
			logger.InfoCtx(ctx).Msg(fmt.Sprintf("Listening on port %s", portStr))
			if certFileEmpty {
				if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					logger.ErrorCtx(ctx).Msg(err.Error())
					os.Exit(1)
				}
				return
			}

			if err := server.ListenAndServeTLS(certFile, keyFile); !errors.Is(err, http.ErrServerClosed) {
				logger.ErrorCtx(ctx).Msg(err.Error())
				os.Exit(1)
			}
		}()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		if err := server.Shutdown(ctx); err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			os.Exit(1)
		}
	},
}

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
