// Package cli is atm's commands, one file each.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// Execute runs atm with os.Args and returns its exit code.
func Execute() int {
	root := &cobra.Command{
		Use:           "atm",
		Short:         "ATM turns an issue into a verified fix: red test, green test, gates, delivery",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	config.AddFlags(root.PersistentFlags())
	root.AddCommand(initCmd(), doctorCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "atm:", err)
		return 1
	}
	return 0
}
