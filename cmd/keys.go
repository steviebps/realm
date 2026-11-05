package cmd

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/auth"
)

// keysCmd manages API keys directly in the storage a server config names, so
// headless installs can mint their first key without the UI.
var keysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage API keys",
	Long: `Manage API keys directly in the storage named by --config.

Keys are also managed by administrators in the UI. A running server notices
keys created here immediately and keys revoked here within 30 seconds. BoltDB
storage is locked by a running server, so stop it first or use the UI.`,
}

var keysCreateCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create an API key and print its token",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: withKeyStore(func(cmd *cobra.Command, args []string, keys *auth.KeyStore) error {
		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		access, _ := flags.GetString("access")
		path, _ := flags.GetString("path")

		key, token, err := keys.Create(cmd.Context(), auth.NewKeyRequest{Name: name, Access: auth.Access(access), Path: path}, "cli")
		if err != nil {
			return err
		}
		cmd.PrintErrf("created %s key %q (%s) for %s — the token is shown only once:\n", key.Access, key.Name, key.ID, key.Path)
		_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
		return err
	}),
}

var keysListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List API keys",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: withKeyStore(func(cmd *cobra.Command, args []string, keys *auth.KeyStore) error {
		list, err := keys.List(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tACCESS\tPATH\tCREATED\tBY")
		for _, k := range list {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Access, k.Path, k.CreatedAt.Format("2006-01-02 15:04"), k.CreatedBy)
		}
		return w.Flush()
	}),
}

var keysRevokeCmd = &cobra.Command{
	Use:          "revoke <id>",
	Short:        "Revoke an API key",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE: withKeyStore(func(cmd *cobra.Command, args []string, keys *auth.KeyStore) error {
		if err := keys.Revoke(cmd.Context(), args[0]); err != nil {
			return err
		}
		cmd.PrintErrf("revoked key %s\n", args[0])
		return nil
	}),
}

// withKeyStore returns a RunE that runs fn against the key store in the
// storage named by --config, closing that storage afterwards.
func withKeyStore(fn func(cmd *cobra.Command, args []string, keys *auth.KeyStore) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		configPath, _ := cmd.Flags().GetString("config")
		if configPath == "" {
			return errors.New("--config must name the server's config file")
		}
		config, err := parseConfig(configPath)
		if err != nil {
			return err
		}
		if config.Server.StorageType == "bigcache" {
			return errors.New("bigcache storage is in memory; manage keys in the UI instead")
		}
		strg, err := openStorage(config.Server)
		if err != nil {
			return err
		}
		defer strg.Close(cmd.Context())
		return fn(cmd, args, auth.NewKeyStore(strg))
	}
}

func init() {
	keysCreateCmd.Flags().String("name", "", "a name describing who uses the key (required)")
	keysCreateCmd.Flags().String("access", string(auth.AccessRead), `"evaluate" for browsers, "read" for server SDKs, "write" for automation`)
	keysCreateCmd.Flags().String("path", "/", "chamber path the key is limited to, including its children")
	_ = keysCreateCmd.MarkFlagRequired("name")

	keysCmd.AddCommand(keysCreateCmd, keysListCmd, keysRevokeCmd)
	rootCmd.AddCommand(keysCmd)
}
