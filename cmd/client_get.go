package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// clientGet represents the client get command
var clientGet = &cobra.Command{
	Use:          "get [path]",
	Short:        "get a chamber",
	Long:         "get retrieves the chamber at the specified path",
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
		flags := cmd.Flags()

		configPath, err := flags.GetString("config")
		if err != nil {
			log.Error().Msg(err.Error())
			return err
		}

		var realmConfig RealmConfig
		if configPath != "" {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				log.Error().Msg(err.Error())
				return err
			}
		}

		addr, _ := cmd.Flags().GetString("address")
		if addr == "" {
			if realmConfig.Client.Address == "" {
				err = errors.New("must specify an address for the realm server")
				log.Error().Msg(err.Error())
				return err
			}
			addr = realmConfig.Client.Address
		}

		c, err := client.NewHttpClient(&client.HttpClientConfig{Address: addr})
		if err != nil {
			log.Error().Msg(err.Error())
			return err
		}

		res, err := c.PerformRequest(ctx, "GET", strings.TrimPrefix(args[0], "/"), nil)
		if err != nil {
			log.Error().Msg(err.Error())
			return err
		}
		defer res.Body.Close()

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			log.Error().Str("error", err.Error()).Msg(fmt.Sprintf("could not read response for getting: %q", args[0]))
			return err
		}

		if len(httpRes.Errors) > 0 {
			log.Error().Msg(fmt.Sprintf("could not get %q: %s", args[0], httpRes.Errors))
			return errors.New(strings.Join(httpRes.Errors, "; "))
		}

		err = utils.WriteInterfaceWith(cmd.OutOrStdout(), httpRes.Data, true)
		if err != nil {
			log.Error().Msg(err.Error())
			return err
		}
		return nil
	},
}

func init() {
	clientCmd.AddCommand(clientGet)
}
