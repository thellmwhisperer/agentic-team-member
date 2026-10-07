package cli

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// doctorCmd is atm doctor: git, the configured agent CLI, gh when origin is on GitHub and .atm.yaml, one line
// each. Every check runs; any failure fails the command.
func doctorCmd() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Check what atm run needs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			failed := 0
			check := func(name, detail string, err error) {
				line := "ok " + name + ": " + detail
				if err != nil {
					failed++
					line = "fail " + name + ": " + err.Error()
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			root, err := toplevel()
			check("git", root, err)
			// Load reads the harness before it checks the keys, so a failing .atm.yaml still names the agent.
			var c config.Config
			home, cfgErr := os.UserHomeDir()
			if cfgErr == nil {
				c, cfgErr = config.Load(cmp.Or(root, "."), home, config.Agent{})
			}
			harness := cmp.Or(c.Harness, "claude")
			path, err := exec.LookPath(harness)
			check(harness, path, err)
			if root != "" && run.GitHubRepo() != "" {
				path, err := exec.LookPath("gh")
				check("gh", path, err)
			}
			check(".atm.yaml", "every key declared", cfgErr)
			if failed > 0 {
				return fmt.Errorf("doctor: %d of the checks failed", failed)
			}
			return nil
		}}
}
