package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// clientPut represents the client put command
var clientPut = &cobra.Command{
	Use:   "put [path]",
	Short: "put a chamber",
	Long:  "put creates or updates the chamber at the specified path",
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
		tracer := otel.Tracer("github.com/steviebps/realm")
		ctx, span := tracer.Start(cmd.Context(), "cmd client put")
		defer span.End()

		var err error
		logger := hclog.Default().Named("realm.client")
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

		c, err := client.NewHttpClient(&client.HttpClientConfig{Address: addr, Logger: logger})
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		emptyChamber, err := json.Marshal(&realm.Chamber{Rules: map[string]*realm.OverrideableRule{}})
		if err != nil {
			logger.Error(fmt.Sprintf("could not marshal empty chamber for putting: %q", args[0]), "error", err.Error())
			os.Exit(1)
		}
		res, err := c.PerformRequest(ctx, "POST", strings.TrimPrefix(args[0], "/"), bytes.NewReader(emptyChamber))
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		defer res.Body.Close()

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			logger.Error(fmt.Sprintf("could not read response for putting: %q", args[0]), "error", err.Error())
			os.Exit(1)
		}

		if len(httpRes.Errors) > 0 {
			logger.Error(fmt.Sprintf("could not put %q: %s", args[0], httpRes.Errors))
			os.Exit(1)
		}

		err = utils.WriteInterfaceWith(cmd.OutOrStdout(), httpRes.Data, true)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
	},
}

func init() {
	clientCmd.AddCommand(clientPut)
}
