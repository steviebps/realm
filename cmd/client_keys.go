package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/auth"
)

// clientKeys manages API keys through a running server. Only administrators
// may: the admin key declared in the server config, in practice, since
// signed-in users manage keys in the UI.
var clientKeys = &cobra.Command{
	Use:   "keys",
	Short: "Manage API keys",
	Long: `Manage the API keys of a running server.

Only an administrator may manage keys, so --token (or $REALM_TOKEN) must be
the admin key declared as auth.adminKey in the server config. Signed-in
administrators can also manage keys in the UI.`,
}

var clientKeysCreate = &cobra.Command{
	Use:          "create",
	Short:        "Create an API key and print its token",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		access, _ := flags.GetString("access")
		path, _ := flags.GetString("path")
		body, err := json.Marshal(auth.NewKeyRequest{Name: name, Access: auth.Access(access), Path: path})
		if err != nil {
			return err
		}
		return runChamberRequest(cmd, chamberRequest{
			verb: "create key", method: http.MethodPost, path: "/v1/keys", endpoint: "/v1/keys", body: body,
			handle: func(data json.RawMessage) error {
				var created struct {
					Key   auth.Key `json:"key"`
					Token string   `json:"token"`
				}
				if err := json.Unmarshal(data, &created); err != nil {
					return err
				}
				k := created.Key
				cmd.PrintErrf("created %s key %q (%s) for %s — the token is shown only once:\n", k.Access, k.Name, k.ID, k.Path)
				_, err := fmt.Fprintln(cmd.OutOrStdout(), created.Token)
				return err
			},
		})
	},
}

var clientKeysList = &cobra.Command{
	Use:          "list",
	Short:        "List API keys",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runChamberRequest(cmd, chamberRequest{
			verb: "list keys", method: http.MethodGet, path: "/v1/keys", endpoint: "/v1/keys",
			handle: func(data json.RawMessage) error {
				var list []auth.Key
				if err := json.Unmarshal(data, &list); err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "ID\tNAME\tACCESS\tPATH\tCREATED\tBY")
				for _, k := range list {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Access, k.Path, k.CreatedAt.Format("2006-01-02 15:04"), k.CreatedBy)
				}
				return w.Flush()
			},
		})
	},
}

var clientKeysRevoke = &cobra.Command{
	Use:          "revoke <id>",
	Short:        "Revoke an API key",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		endpoint := "/v1/keys/" + url.PathEscape(args[0])
		if err := runChamberRequest(cmd, chamberRequest{verb: "revoke key", method: http.MethodDelete, path: endpoint, endpoint: endpoint}); err != nil {
			return err
		}
		cmd.PrintErrf("revoked key %s\n", args[0])
		return nil
	},
}

func init() {
	clientKeysCreate.Flags().String("name", "", "a name describing who uses the key (required)")
	clientKeysCreate.Flags().String("access", string(auth.AccessRead), `"evaluate" for browsers, "read" for server SDKs, "write" for automation`)
	clientKeysCreate.Flags().String("path", "/", "chamber path the key is limited to, including its children")
	_ = clientKeysCreate.MarkFlagRequired("name")

	clientKeys.AddCommand(clientKeysCreate, clientKeysList, clientKeysRevoke)
	clientCmd.AddCommand(clientKeys)
}
