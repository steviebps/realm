package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	realmhttp "github.com/steviebps/realm/http"
	"github.com/steviebps/realm/internal/logger"
	realm "github.com/steviebps/realm/pkg"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmdError = logger.ErrorWithPrefix("error running server command: ")

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server for serving http requests",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := realm.Float64Value("port", 3000)

		storage, err := storage.NewRealmFile("./tmp")
		if err != nil {
			serverCmdError(err.Error())
		}

		handler := realmhttp.Handler(storage)

		log.Println("Listening on :", port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", int(port)), handler); err != nil {
			serverCmdError(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
