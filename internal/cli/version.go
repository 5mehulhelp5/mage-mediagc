package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/version"
)

func newVersionCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			if jsonOut {
				enc := json.NewEncoder(a.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintf(a.stdout, "mage-mediagc %s\n", info.Version)
			fmt.Fprintf(a.stdout, "  commit   %s\n", info.Commit)
			fmt.Fprintf(a.stdout, "  built    %s\n", info.BuildDate)
			fmt.Fprintf(a.stdout, "  go       %s\n", info.GoVersion)
			fmt.Fprintf(a.stdout, "  platform %s\n", info.Platform)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print as JSON")
	return cmd
}
