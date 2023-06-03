package cmd

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	realmhttp "github.com/steviebps/realm/http"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server",
	Run: func(cmd *cobra.Command, args []string) {
		flags := cmd.Flags()
		debug, _ := flags.GetBool("debug")

		level := hclog.Info
		if debug {
			level = hclog.Debug
		}

		logger := hclog.New(&hclog.LoggerOptions{
			Name:                 "realm.server",
			Level:                level,
			Output:               cmd.OutOrStdout(),
			TimeFn:               time.Now,
			ColorHeaderAndFields: true,
			Color:                hclog.AutoColor,
		})

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		if configPath == "" {
			logger.Error("config must be specified")
			os.Exit(1)
		}

		realmConfig, err := parseConfig(configPath)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		serverConfig := realmConfig.Server

		portStr, err := flags.GetString("port")
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		if portStr == "" {
			portStr = serverConfig.Port
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		certFile := serverConfig.CertFile
		keyFile := serverConfig.KeyFile
		storageType := serverConfig.StorageType

		certFileEmpty := certFile == ""
		keyFileEmpty := keyFile == ""
		if certFileEmpty != keyFileEmpty {
			logger.Error("certFile must be used in conjuction with keyFile")
			os.Exit(1)
		}

		logger.Info("Server options", "port", portStr, "certFile", certFile, "keyFile", keyFile, "storage", storageType, "debug", debug)

		strgCreator, exists := storage.StorageOptions[storageType]
		if !exists {
			logger.Error(fmt.Sprintf("storage type %q does not exist", storageType))
			os.Exit(1)
		}
		stg, err := strgCreator(serverConfig.StorageOptions)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		handler, err := realmhttp.NewHandler(realmhttp.HandlerConfig{Storage: stg, Logger: logger, RequestTimeout: 5 * time.Second})
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		if certFileEmpty {
			logger.Info("Listening on", "port", portStr)
			if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
				logger.Error(err.Error())
				os.Exit(1)
			}
			return
		}

		logger.Info("Listening on", "port", portStr)
		if err := http.ListenAndServeTLS(fmt.Sprintf(":%d", port), certFile, keyFile, handler); err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	serverCmd.Flags().String("port", "", "port to run server on")
	rootCmd.AddCommand(serverCmd)
}
