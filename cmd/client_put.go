package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
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
	Args:         chamberPathArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		r := chamberRequest{verb: "put", method: "POST", path: args[0], header: http.Header{"Content-Type": {"application/json"}}, print: true}
		if file, _ := cmd.Flags().GetString("file"); file != "" {
			body, err := readChamberFile(cmd, file)
			if err != nil {
				ctx := cmd.Context()
				logging.Ctx(ctx).ErrorCtx(ctx).Msg(err.Error())
				return err
			}
			r.body = body
		} else {
			// Without --file, put only creates an empty chamber: it must never
			// replace an existing chamber's flags with nothing.
			r.body = emptyChamber
			r.header.Set("If-None-Match", "*")
			r.statusErr = func(status int) error {
				if status == http.StatusPreconditionFailed {
					return fmt.Errorf("%s already exists; pass --file to replace it", args[0])
				}
				return nil
			}
		}
		return runChamberRequest(cmd, r)
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
