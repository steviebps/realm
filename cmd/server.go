package cmd

import (
	"fmt"
	"net/http"
	"os"

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
		logger.Info("Server options", "port", port, "path", path, "certFile", certFile, "keyFile", keyFile)

		storage, err := storage.NewFileStorage(path, logger)
		if err != nil {
			realmCore.Logger().Error(err.Error())
		}

		handler := realmhttp.NewHandler(realmhttp.HandlerConfig{Realm: realmCore, Storage: storage})
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
