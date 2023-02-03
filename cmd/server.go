package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	realmhttp "github.com/steviebps/realm/http"
	"github.com/steviebps/realm/internal/logger"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmdError = logger.ErrorWithPrefix("error running server command: ")

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server for serving http requests",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := realmCore.Float64Value("port", 3000)
		path, _ := realmCore.StringValue("path", "./.realm")
		certFile, _ := realmCore.StringValue("certFile", "")
		keyFile, _ := realmCore.StringValue("keyFile", "")
		realmCore.Logger().Info("Server options", "port", port, "path", path)

		storage, err := storage.NewFileStorage(path)
		if err != nil {
			serverCmdError(err.Error())
		}
		handler := realmhttp.NewHandler(realmhttp.HandlerConfig{Realm: &realmCore, Storage: storage})

		realmCore.Logger().Info("Listening on", "port", port)
		if err := http.ListenAndServeTLS(fmt.Sprintf(":%d", int(port)), certFile, keyFile, handler); err != nil {
			serverCmdError(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
