package cli

import (
	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func answerCmd() *cobra.Command {
	return &cobra.Command{Use: "answer <run> <approve|fix|stop>",
		Short: "Answer a failed check: approve delivery, give the agent one fix turn, or stop",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := toplevel()
			if err == nil {
				err = run.Answer(root, args[0], args[1])
			}
			return err
		}}
}
