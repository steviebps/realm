package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/spf13/cobra"
	realmhttp "github.com/steviebps/realm/http"
	realm "github.com/steviebps/realm/pkg"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server for serving http requests",
	Run: func(cmd *cobra.Command, args []string) {
		realmCore := cmd.Context().Value("core").(*realm.Realm)
		logger := realmCore.Logger()
		port, _ := realmCore.Float64Value("port", 3000)
		path, _ := realmCore.StringValue("path", "./.realm")
		certFile, _ := realmCore.StringValue("certFile", "")
		keyFile, _ := realmCore.StringValue("keyFile", "")
		storageType, _ := realmCore.StringValue("storage", "file")
		logger.Info("Server options", "port", port, "path", path, "certFile", certFile, "keyFile", keyFile)

		var s storage.Storage
		var err error
		switch storageType {
		case "bigcache":
			s, err = storage.NewBigCacheStorage(logger, bigcache.Config{
				// number of shards (must be a power of 2)
				Shards: 64,

				// time after which entry can be evicted
				LifeWindow: 1 * time.Minute,

				// Interval between removing expired entries (clean up).
				// If set to <= 0 then no action is performed.
				// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
				CleanWindow: 1 * time.Minute,
			})
		case "file":
			s, err = storage.NewFileStorage(path, logger)
		default:
			s, err = storage.NewFileStorage(path, logger)
		}

		if err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}

		handler, err := realmhttp.NewHandler(realmhttp.HandlerConfig{Realm: realmCore, Storage: s})
		if err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}

		realmCore.Logger().Info("Listening on", "port", port)
		if err := http.ListenAndServeTLS(fmt.Sprintf(":%d", int(port)), certFile, keyFile, handler); err != nil {
			realmCore.Logger().Error(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
