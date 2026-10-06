package cli

import (
	"cmp"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/project"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check git, the configured agent CLI and, when origin is on GitHub, gh",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, failed := cmd.OutOrStdout(), 0
			check := func(name, detail string, err error) {
				status := "ok"
				if err != nil {
					status, detail, failed = "FAIL", err.Error(), failed+1
				}
				_, _ = fmt.Fprintf(out, "%-4s  %-8s  %s\n", status, name, detail)
			}
			gitPath, err := exec.LookPath("git")
			check("git", gitPath, err)
			// Outside a repository there is no .atm.yaml layer, and no origin to check gh for.
			p, err := project.Find(".")
			if err != nil {
				check("repo", "", err)
			}
			cfg, err := config.Load(p.Root, cmd.Flags())
			if err != nil {
				check("config", "", err)
			}
			agentPath, err := exec.LookPath(cfg.Harness)
			check(cfg.Harness, fmt.Sprintf("%s (model %s, effort %s)", agentPath, cmp.Or(cfg.Model, "default"),
				cmp.Or(cfg.Effort, "default")), err)
			if p.GitHub != "" {
				ghPath, err := exec.LookPath("gh")
				check("gh", fmt.Sprintf("%s (%s)", ghPath, p.GitHub), err)
			}
			if failed > 0 {
				return fmt.Errorf("%d checks failed", failed)
			}
			return nil
		},
	}
}
