package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
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
