package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func steerCmd() *cobra.Command {
	return &cobra.Command{Use: "steer <run> <message>",
		Short: "Send a message to a run's working agent, which reads it before its next model call (pi only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err == nil {
				err = run.Steer(root, args[0], args[1])
			}
			if err == nil {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "the agent of", args[0], "took the message")
			}
			return err
		}}
}
