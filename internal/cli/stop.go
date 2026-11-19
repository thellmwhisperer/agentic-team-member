package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func stopCmd() *cobra.Command {
	return &cobra.Command{Use: "stop <run>",
		Short: "Stop a run before its delivery: kill its agent and every process it started; the run fails",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err == nil {
				err = run.Stop(root, args[0])
			}
			if err == nil {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "stopped", args[0])
			}
			return err
		}}
}
