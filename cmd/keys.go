package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/auth"
)

// keysCmd manages API keys through a running server's /v1/keys API, like the
// UI does. Only bootstrap reaches storage directly, to mint the first key.
var keysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage API keys",
	Long: `Manage API keys through a realm server's API.

create, list, and revoke call the server named by --address (or the config
file's client.address) with an admin key from --token, $` + TokenEnv + `, or the
config file's client.token. An admin key manages the keys at or beneath its
own path; administrators can also manage keys in the UI.

To get the first admin key on a server nobody has signed in to, run
"realm keys bootstrap" with the server's config file.`,
}

var keysCreateCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create an API key and print its token",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var created struct {
			Key   auth.Key `json:"key"`
			Token string   `json:"token"`
		}
		if err := callKeys(cmd, http.MethodPost, "/v1/keys", keyRequest(cmd), &created); err != nil {
			return err
		}
		return printToken(cmd, created.Key, created.Token)
	},
}

var keysListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List the API keys you may manage",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var list []auth.Key
		if err := callKeys(cmd, http.MethodGet, "/v1/keys", nil, &list); err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tACCESS\tPATH\tCREATED\tBY")
		for _, k := range list {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Access, k.Path, k.CreatedAt.Format("2006-01-02 15:04"), k.CreatedBy)
		}
		return w.Flush()
	},
}

var keysRevokeCmd = &cobra.Command{
	Use:          "revoke <id>",
	Short:        "Revoke an API key",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := callKeys(cmd, http.MethodDelete, "/v1/keys/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		cmd.PrintErrf("revoked key %s\n", args[0])
		return nil
	},
}

var keysBootstrapCmd = &cobra.Command{
	Use:          "bootstrap",
	Short:        "Create the first API key directly in the server's storage",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Create an API key by writing it directly to the storage named by the
server config file in --config, with no running server and no sign-in. Use it
once, to get the first admin key (or to recover after losing every admin
key), then manage keys with "realm keys create", "list", and "revoke".

Run it before the server starts or while it is stopped. A running server
locks boltdb storage, and one with a cacheable backend can take minutes to see
the new key. With postgres or file storage it is safe while servers run.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		configPath, _ := cmd.Flags().GetString("config")
		if configPath == "" {
			return errors.New("--config must name the server's config file")
		}
		config, err := parseConfig(configPath)
		if err != nil {
			return err
		}
		if config.Server.StorageType == "bigcache" {
			return errors.New("bigcache storage is in memory, so a key written here would vanish; sign in to the UI instead")
		}
		strg, err := openStorage(config.Server)
		if err != nil {
			return err
		}
		defer strg.Close(cmd.Context())

		key, token, err := auth.NewKeyStore(strg).Create(cmd.Context(), keyRequest(cmd), "cli bootstrap")
		if err != nil {
			return err
		}
		return printToken(cmd, key, token)
	},
}

// keyRequest reads the --name, --access, and --path flags.
func keyRequest(cmd *cobra.Command) auth.NewKeyRequest {
	flags := cmd.Flags()
	name, _ := flags.GetString("name")
	access, _ := flags.GetString("access")
	path, _ := flags.GetString("path")
	return auth.NewKeyRequest{Name: name, Access: auth.Access(access), Path: path}
}

// printToken reports a created key on stderr and prints only its token on
// stdout, so scripts can capture it.
func printToken(cmd *cobra.Command, key auth.Key, token string) error {
	cmd.PrintErrf("created %s key %q (%s) for %s — the token is shown only once:\n", key.Access, key.Name, key.ID, key.Path)
	_, err := fmt.Fprintln(cmd.OutOrStdout(), token)
	return err
}

// callKeys sends body (when not nil) as JSON to the server's key API and
// decodes the response's data into out (when not nil).
func callKeys(cmd *cobra.Command, method, apiPath string, body, out any) error {
	ctx := cmd.Context()
	c, err := newClient(cmd)
	if err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := c.NewAPIRequest(ctx, method, apiPath, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var env api.HTTPErrorAndDataResponse
	decodeErr := json.NewDecoder(res.Body).Decode(&env)
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		env = api.HTTPErrorAndDataResponse{}
	}
	switch {
	case res.StatusCode == http.StatusNotFound && len(env.Errors) == 0:
		// the route only exists when the server requires authentication
		return fmt.Errorf("%s has no API keys: its config has no \"auth\" block", req.URL.Host)
	case res.StatusCode >= http.StatusBadRequest:
		msg := http.StatusText(res.StatusCode)
		if len(env.Errors) > 0 {
			msg = strings.Join(env.Errors, "; ")
		}
		if res.StatusCode == http.StatusUnauthorized && req.Header.Get("Authorization") == "" {
			msg += " (pass an admin key with --token or $" + TokenEnv + ")"
		}
		return fmt.Errorf("%s %s: %s", method, apiPath, msg)
	case decodeErr != nil && !errors.Is(decodeErr, io.EOF):
		return fmt.Errorf("could not read the server's response: %w", decodeErr)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func init() {
	for _, c := range []*cobra.Command{keysCreateCmd, keysBootstrapCmd} {
		c.Flags().String("path", "/", "chamber path the key is limited to, including its children")
	}
	keysCreateCmd.Flags().String("name", "", "a name describing who uses the key (required)")
	keysCreateCmd.Flags().String("access", string(auth.AccessRead), `"evaluate" for browsers, "read" for server SDKs, "write" for automation, "admin" to manage keys`)
	_ = keysCreateCmd.MarkFlagRequired("name")
	keysBootstrapCmd.Flags().String("name", "bootstrap", "a name describing who uses the key")
	keysBootstrapCmd.Flags().String("access", string(auth.AccessAdmin), "the key's access; the first key is usually an admin key")

	keysCmd.AddCommand(keysCreateCmd, keysListCmd, keysRevokeCmd, keysBootstrapCmd)
	rootCmd.AddCommand(keysCmd)
}
