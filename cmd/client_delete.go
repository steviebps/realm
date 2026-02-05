package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// clientDelete represents the client delete command
var clientDelete = &cobra.Command{
	Use:          "delete [path]",
	Short:        "delete a chamber",
	Long:         "delete a chamber at the specified path",
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
		ctx, span := tracer.Start(cmd.Context(), "cmd client delete")
		defer span.End()
		logger := logging.Ctx(ctx)

		var err error
		flags := cmd.Flags()

		configPath, err := flags.GetString("config")
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}

		var realmConfig RealmConfig
		if configPath != "" {
			realmConfig, err = parseConfig(configPath)
			if err != nil {
				logger.ErrorCtx(ctx).Msg(err.Error())
				return err
			}
		}

		addr, _ := cmd.Flags().GetString("address")
		if addr == "" {
			if realmConfig.Client.Address == "" {
				err = errors.New("must specify an address for the realm server")
				logger.ErrorCtx(ctx).Msg(err.Error())
				return err
			}
			addr = realmConfig.Client.Address
		}

		c, err := client.NewHttpClient(&client.HttpClientConfig{Address: addr})
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}

		res, err := c.PerformRequest(ctx, "DELETE", strings.TrimPrefix(args[0], "/"), nil)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}
		defer res.Body.Close()

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			logger.ErrorCtx(ctx).Str("error", err.Error()).Msg(fmt.Sprintf("could not read response for deleting: %q", args[0]))
			return err
		}

		if len(httpRes.Errors) > 0 {
			logger.ErrorCtx(ctx).Msg(fmt.Sprintf("could not delete %q: %s", args[0], httpRes.Errors))
			return errors.New(strings.Join(httpRes.Errors, "; "))
		}

		logger.InfoCtx(ctx).Msg(fmt.Sprintf("successfully deleted %q", args[0]))
		return nil
	},
}

func init() {
	clientCmd.AddCommand(clientDelete)
}
