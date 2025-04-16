package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
)

// clientDelete represents the client delete command
var clientDelete = &cobra.Command{
	Use:   "delete [path]",
	Short: "delete a chamber",
	Long:  "delete a chamber at the specified path",
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		if err := storage.ValidatePath(args[0]); err != nil {
			return fmt.Errorf("invalid path specified: %s, %w", args[0], err)
		}

		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		logger := hclog.Default().Named("client")
		flags := cmd.Flags()

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		var realmConfig RealmConfig
		if configPath != "" {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				logger.Error(err.Error())
				os.Exit(1)
			}
		}

		addr, _ := cmd.Flags().GetString("address")
		if addr == "" {
			if realmConfig.Client.Address == "" {
				logger.Error("must specify an address for the realm server")
				os.Exit(1)
			}
			addr = realmConfig.Client.Address
		}

		c, err := client.NewClient(&client.ClientConfig{Address: addr, Logger: logger})
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		res, err := c.PerformRequest("DELETE", strings.TrimPrefix(args[0], "/"), nil)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		defer res.Body.Close()

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			logger.Error(fmt.Sprintf("could not read response for deleting: %q", args[0]), "error", err.Error())
			os.Exit(1)
		}

		if len(httpRes.Errors) > 0 {
			logger.Error(fmt.Sprintf("could not delete %q: %s", args[0], httpRes.Errors))
			os.Exit(1)
		}

		logger.Info(fmt.Sprintf("successfully deleted %q", args[0]))
	},
}

func init() {
	clientCmd.AddCommand(clientDelete)
}
