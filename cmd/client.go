package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
)

// TokenEnv names the environment variable the CLI reads an API key from.
const TokenEnv = "REALM_TOKEN"

// AddressEnv names the environment variable the CLI reads the server address
// from.
const AddressEnv = "REALM_ADDRESS"

// clientCmd represents the client command
var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Realm server client",
	Long:  "Client for invoking various operations on a realm server",
}

func init() {
	rootCmd.AddCommand(clientCmd)
	rootCmd.PersistentFlags().StringP("address", "a", "", "address of realm server (or set $"+AddressEnv+")")
	rootCmd.PersistentFlags().String("token", "", "API key for the realm server (or set $"+TokenEnv+")")
}

// newClient returns an HTTP client for the server named by --address,
// $REALM_ADDRESS, or the config file's client.address, authenticated with
// --token, $REALM_TOKEN, or the config file's client.token. The config file is
// optional; see findConfig for where it is looked for.
func newClient(cmd *cobra.Command) (*client.HttpClient, error) {
	flags := cmd.Flags()
	realmConfig, configPath, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	if configPath != "" {
		logging.Ctx(cmd.Context()).DebugCtx(cmd.Context()).Str("config", configPath).Msg("using config file")
	}

	addr, _ := flags.GetString("address")
	if addr == "" {
		addr = os.Getenv(AddressEnv)
	}
	if addr == "" {
		addr = realmConfig.Client.Address
	}
	if addr == "" {
		return nil, errors.New("must specify an address for the realm server: pass --address, set $" + AddressEnv + ", or set client.address in the config file")
	}

	token, _ := flags.GetString("token")
	if token == "" {
		token = os.Getenv(TokenEnv)
	}
	if token == "" {
		token = os.ExpandEnv(realmConfig.Client.Token)
	}
	return client.NewHttpClient(&client.HttpClientConfig{Address: addr, Token: token})
}

// chamberPathArg validates the single chamber path argument the client
// subcommands take, showing usage only when the argument count is wrong.
func chamberPathArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		cmd.SilenceUsage = false
		return err
	}
	if err := storage.ValidatePath(args[0]); err != nil {
		return fmt.Errorf("invalid path specified: %s, %w", args[0], err)
	}
	return nil
}

// chamberRequest describes the one request a client subcommand makes.
type chamberRequest struct {
	// verb names the operation in spans and messages ("get", "list", ...).
	verb   string
	method string
	// path is the chamber path; query, if set, is appended to the request.
	path   string
	query  string
	body   []byte
	header http.Header
	// statusErr, if set, may turn a response status into an error before the
	// body is read.
	statusErr func(status int) error
	// print writes the response's data to standard output.
	print bool
}

// runChamberRequest performs r against the server named by cmd's flags,
// returning the server's errors and printing the response's data if r.print.
func runChamberRequest(cmd *cobra.Command, r chamberRequest) error {
	ctx, span := otel.Tracer("github.com/steviebps/realm").Start(cmd.Context(), "cmd client "+r.verb)
	defer span.End()
	logger := logging.Ctx(ctx)
	fail := func(err error) error {
		logger.ErrorCtx(ctx).Msg(err.Error())
		return err
	}

	c, err := newClient(cmd)
	if err != nil {
		return fail(err)
	}
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	target := r.path
	if r.query != "" {
		target += "?" + r.query
	}
	req, err := c.NewRequest(ctx, r.method, target, body)
	if err != nil {
		return fail(err)
	}
	maps.Copy(req.Header, r.header)
	res, err := c.Do(req)
	if err != nil {
		return fail(err)
	}
	defer res.Body.Close()
	if r.statusErr != nil {
		if err := r.statusErr(res.StatusCode); err != nil {
			return err
		}
	}

	var httpRes api.HTTPErrorAndDataResponse
	if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
		logger.ErrorCtx(ctx).Str("error", err.Error()).Msgf("could not read response for %s: %q", r.verb, r.path)
		return err
	}
	if len(httpRes.Errors) > 0 {
		logger.ErrorCtx(ctx).Msgf("could not %s %q: %s", r.verb, r.path, httpRes.Errors)
		return errors.New(strings.Join(httpRes.Errors, "; "))
	}
	if r.print {
		if err := utils.WriteInterfaceWith(cmd.OutOrStdout(), httpRes.Data, true); err != nil {
			return fail(err)
		}
	}
	return nil
}
