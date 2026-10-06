// Package cli is atm's commands, one file each.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
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
	root.AddCommand(initCmd(), doctorCmd(), runCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "atm:", err)
		if errors.Is(err, run.ErrNotStarted) {
			return 2
		}
		return 1
	}
	return 0
}
