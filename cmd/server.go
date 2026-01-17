package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	realmhttp "github.com/steviebps/realm/http"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server",
	PreRun: func(cmd *cobra.Command, args []string) {
		devMode, _ := cmd.Flags().GetBool("dev")
		if !devMode {
			cmd.MarkFlagRequired("config")
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		flags := cmd.Flags()
		debug, _ := flags.GetBool("debug")

		configPath, err := flags.GetString("config")
		if err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}

		devMode, _ := flags.GetBool("dev")
		if !devMode && configPath == "" {
			log.Error().Msg("config must be specified")
			os.Exit(1)
		}
		var realmConfig RealmConfig

		if devMode {
			realmConfig = NewDefaultServerConfig()
		} else {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				log.Error().Msg(err.Error())
				os.Exit(1)
			}
		}

		serverConfig := realmConfig.Server

		portStr, err := flags.GetString("port")
		if err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}

		if portStr == "" {
			portStr = serverConfig.Port
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}
		certFile := serverConfig.CertFile
		keyFile := serverConfig.KeyFile
		storageType := serverConfig.StorageType

		certFileEmpty := certFile == ""
		keyFileEmpty := keyFile == ""
		if certFileEmpty != keyFileEmpty {
			log.Error().Msg("certFile must be used in conjuction with keyFile")
			os.Exit(1)
		}

		log.Info().Str("port", portStr).Str("certFile", certFile).Str("keyFile", keyFile).Str("storage", storageType).Bool("inheritable", serverConfig.Inheritable).Bool("debug", debug).Msg("server options")

		strgCreator, exists := storage.StorageOptions[storageType]
		if !exists {
			log.Error().Msg(fmt.Sprintf("storage type %q does not exist", storageType))
			os.Exit(1)
		}

		options := []interface{}{}
		for k, v := range serverConfig.StorageOptions {
			options = append(options, k, v)
		}
		if len(options) > 0 {
			log.Debug().Any("options", options).Msg("storage options")
		}

		stg, err := strgCreator(serverConfig.StorageOptions)
		if err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}

		if serverConfig.Inheritable {
			stg, err = storage.NewInheritableStorage(stg)
			if err != nil {
				log.Error().Msg(err.Error())
				os.Exit(1)
			}
		}

		handler, err := realmhttp.NewHandler(realmhttp.HandlerConfig{Storage: stg, RequestTimeout: realmhttp.DefaultHandlerTimeout})
		if err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}

		server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: handler}

		go func() {
			log.Info().Msg(fmt.Sprintf("Listening on port %s", portStr))
			if certFileEmpty {
				if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					log.Error().Msg(err.Error())
					os.Exit(1)
				}
				return
			}

			if err := server.ListenAndServeTLS(certFile, keyFile); !errors.Is(err, http.ErrServerClosed) {
				log.Error().Msg(err.Error())
				os.Exit(1)
			}
		}()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		if err := server.Shutdown(ctx); err != nil {
			log.Error().Msg(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	serverCmd.Flags().String("port", "", "port to run server on")
	serverCmd.Flags().Bool("dev", false, "run server in dev mode")
	rootCmd.AddCommand(serverCmd)
}
