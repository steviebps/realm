package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// clientPut represents the client put command
var clientPut = &cobra.Command{
	Use:   "put [path]",
	Short: "put a chamber",
	Long: `put creates the chamber at the specified path.

Without --file it creates an empty chamber, and fails if one already exists.
With --file it creates or replaces the chamber with the file's contents (a JSON
chamber; "-" reads standard input), validated before it is sent.`,
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
		ctx, span := tracer.Start(cmd.Context(), "cmd client put")
		defer span.End()
		logger := logging.Ctx(ctx)

		c, err := newClient(cmd)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}

		// Without --file, put only creates an empty chamber: it must never
		// replace an existing chamber's flags with nothing.
		body, err := json.Marshal(&realm.Chamber{Rules: map[string]*realm.Flag{}})
		if err != nil {
			return err
		}
		file, _ := cmd.Flags().GetString("file")
		if file != "" {
			if body, err = readChamberFile(cmd, file); err != nil {
				logger.ErrorCtx(ctx).Msg(err.Error())
				return err
			}
		}

		req, err := c.NewRequest(ctx, "POST", strings.TrimPrefix(args[0], "/"), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if file == "" {
			req.Header.Set("If-None-Match", "*")
		}
		res, err := c.Do(req)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}
		defer res.Body.Close()
		if file == "" && res.StatusCode == http.StatusPreconditionFailed {
			return fmt.Errorf("%s already exists; pass --file to replace it", args[0])
		}

		var httpRes api.HTTPErrorAndDataResponse
		if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
			logger.ErrorCtx(ctx).Str("error", err.Error()).Msg(fmt.Sprintf("could not read response for putting: %q", args[0]))
			return err
		}

		if len(httpRes.Errors) > 0 {
			logger.ErrorCtx(ctx).Msg(fmt.Sprintf("could not put %q: %s", args[0], httpRes.Errors))
			return errors.New(strings.Join(httpRes.Errors, "; "))
		}

		err = utils.WriteInterfaceWith(cmd.OutOrStdout(), httpRes.Data, true)
		if err != nil {
			logger.ErrorCtx(ctx).Msg(err.Error())
			return err
		}
		return nil
	},
}

// readChamberFile reads and validates the chamber in file ("-" for standard
// input), returning its bytes.
func readChamberFile(cmd *cobra.Command, file string) ([]byte, error) {
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(cmd.InOrStdin())
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read chamber: %w", err)
	}
	if _, err := realm.ParseChamber(b); err != nil {
		return nil, fmt.Errorf("invalid chamber in %s: %w", file, err)
	}
	return b, nil
}

func init() {
	clientPut.Flags().StringP("file", "f", "", `chamber JSON to store ("-" for standard input); replaces the chamber`)
	clientCmd.AddCommand(clientPut)
}
