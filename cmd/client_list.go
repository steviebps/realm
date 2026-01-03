package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// clientList represents the client list command
var clientList = &cobra.Command{
	Use:          "list [path]",
	Short:        "list chambers",
	Long:         "list chambers at the specified path",
	SilenceUsage: true,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			cmd.SilenceUsage = false
			return err
		}
		if err := storage.ValidatePath(args[0]); err != nil {
			return fmt.Errorf("invalid path specified: %s, %w", args[0], err)
		}

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		tracer := otel.Tracer("github.com/steviebps/realm")
		ctx, span := tracer.Start(cmd.Context(), "cmd client get")
		defer span.End()

		var err error
		logger := hclog.Default().Named("realm.client")
		flags := cmd.Flags()

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.Error(err.Error())
			return err
		}

		var realmConfig RealmConfig
		if configPath != "" {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				logger.Error(err.Error())
				return err
			}
		}

		addr, _ := cmd.Flags().GetString("address")
		if addr == "" {
			if realmConfig.Client.Address == "" {
				logger.Error("must specify an address for the realm server")
				return errors.New("must specify an address for the realm server")
			}
			addr = realmConfig.Client.Address
		}

		c, err := client.NewHttpClient(&client.HttpClientConfig{Address: addr, Logger: logger})
		if err != nil {
			logger.Error(err.Error())
			return err
		}

		res, err := c.PerformRequest(ctx, "GET", strings.TrimPrefix(args[0], "/")+"?list=true", nil)
		if err != nil {
			logger.Error(err.Error())
			return err
		}
		defer res.Body.Close()

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			logger.Error(fmt.Sprintf("could not read response for listing: %q", args[0]), "error", err.Error())
			return err
		}

		if len(httpRes.Errors) > 0 {
			logger.Error(fmt.Sprintf("could not list %q: %s", args[0], httpRes.Errors))
			return errors.New(strings.Join(httpRes.Errors, "; "))
		}

		err = utils.WriteInterfaceWith(cmd.OutOrStdout(), httpRes.Data, true)
		if err != nil {
			logger.Error(err.Error())
			return err
		}
		return nil
	},
}

func init() {
	clientCmd.AddCommand(clientList)
}
