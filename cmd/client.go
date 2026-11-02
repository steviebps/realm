package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/client"
)

// TokenEnv names the environment variable the CLI reads an API key from.
const TokenEnv = "REALM_TOKEN"

// clientCmd represents the client command
var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Realm server client",
	Long:  "Client for invoking various operations on a realm server",
}

func init() {
	rootCmd.AddCommand(clientCmd)
	rootCmd.PersistentFlags().StringP("address", "a", "", "address of realm server")
	rootCmd.PersistentFlags().String("token", "", "API key for the realm server (or set $"+TokenEnv+")")
}

// newClient returns an HTTP client for the server named by --address (or the
// config file's client.address), authenticated with --token, $REALM_TOKEN, or
// the config file's client.token.
func newClient(cmd *cobra.Command) (*client.HttpClient, error) {
	flags := cmd.Flags()
	var realmConfig RealmConfig
	if configPath, _ := flags.GetString("config"); configPath != "" {
		var err error
		if realmConfig, err = parseConfig(configPath); err != nil {
			return nil, err
		}
	}

	addr, _ := flags.GetString("address")
	if addr == "" {
		addr = realmConfig.Client.Address
	}
	if addr == "" {
		return nil, errors.New("must specify an address for the realm server")
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
