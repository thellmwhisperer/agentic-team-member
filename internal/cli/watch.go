package cli

import (
	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// watchCmd is atm watch: until killed, a line per event of the runs bound to it, for the session that launches
// them to read under a monitor that wakes it on each line.
func watchCmd() *cobra.Command {
	c := &cobra.Command{Use: "watch", Short: "Print each event of the runs bound to this watcher, until killed",
		Args: cobra.NoArgs}
	adopt := c.Flags().String("adopt", "", "a run to take over, its state printed first")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		root, err := toplevel()
		if err != nil {
			return err
		}
		return run.Watch(cmd.Context(), root, *adopt, cmd.OutOrStdout())
	}
	return c
}
