package cmd

import (
	"github.com/spf13/cobra"
)

// clientCmd represents the client command
var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Realm server client",
	Long:  "Client for invoking various operations on a realm server",
}

func init() {
	rootCmd.AddCommand(clientCmd)
	rootCmd.PersistentFlags().StringP("address", "a", "", "address of realm server")
}
