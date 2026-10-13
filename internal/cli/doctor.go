package cli

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// helloLimit bounds atm doctor --agent's hello.
var helloLimit = 2 * time.Minute

// doctorCmd is atm doctor: git, the configured agent CLI, gh when origin is on GitHub and .atm.yaml, one line
// each, then, with --agent, the agent's answer to a hello. Every check runs; any failure fails the command.
func doctorCmd() *cobra.Command {
	var hello bool
	c := &cobra.Command{Use: "doctor", Short: "Check what atm run needs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			failed := 0
			check := func(name, detail string, err error) {
				line := "ok " + name + ": " + detail
				if err != nil {
					failed++
					line = "fail " + name + ": " + err.Error()
				}
				line = strings.Join(strings.Fields(line), " ")
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
			last := "run atm doctor --agent to say hello to " + harness
			if hello {
				if cfgErr != nil {
					failed++
					last = "FAIL agent: " + cmp.Or(c.Harness, "(none)") + ": " + cfgErr.Error()
				} else {
					took, err := run.Hello(c.Agent, helloLimit)
					last = "ok agent: " + harness + " " + c.Model + " answered in " + run.Human(took)
					if err != nil {
						failed++
						last = "FAIL agent: " + harness + ": " + err.Error()
					}
				}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(strings.Fields(last), " "))
			if failed > 0 {
				return fmt.Errorf("doctor: %d of the checks failed", failed)
			}
			return nil
		}}
	c.Flags().BoolVar(&hello, "agent", false, "say hello to the agent the way a run does")
	return c
}
