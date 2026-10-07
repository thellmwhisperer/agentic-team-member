package cli

import (
	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// runCmd is atm run; run.Run parses its own flags.
func runCmd() *cobra.Command {
	return &cobra.Command{Use: "run", Short: "Fix an issue: " + run.Usage, DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run.Run(args, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}}
}
