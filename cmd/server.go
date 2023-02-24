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
	realm "github.com/steviebps/realm/pkg"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server",
	Run: func(cmd *cobra.Command, args []string) {
		logger := hclog.Default().Named("realm")
		flags := cmd.Flags()

		portStr, err := flags.GetString("port")
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		if configPath == "" {
			logger.Error("config must be specified")
			os.Exit(1)
		}

		realmConfig, err := parseConfig[RealmConfig](configPath)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		serverConfig := realmConfig.Server

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

		logger.Info("Server options", "port", portStr, "certFile", certFile, "keyFile", keyFile, "storage", storageType)

		stgCreator, exists := storage.StorageOptions[storageType]
		if !exists {
			logger.Error(fmt.Sprintf("storage type %q does not exist", storageType))
			os.Exit(1)
		}
		stg, err := stgCreator(serverConfig.StorageOptions, logger)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		realmCore := realm.NewRealm(realm.RealmOptions{Storage: stg, Logger: logger})

		if err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}

		handler, err := realmhttp.NewHandler(realmhttp.HandlerConfig{Realm: realmCore, RequestTimeout: 1 * time.Second})
		if err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}

		realmCore.Logger().Info("Listening on", "port", portStr)
		if err := http.ListenAndServeTLS(fmt.Sprintf(":%d", port), certFile, keyFile, handler); err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	serverCmd.Flags().String("port", "", "port to run server on")
	rootCmd.AddCommand(serverCmd)
}
