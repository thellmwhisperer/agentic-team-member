package cli

import (
	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/project"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func runCmd() *cobra.Command {
	o := run.Options{}
	timeout := 0
	cmd := &cobra.Command{
		Use:   "run <issue.md>",
		Short: "Fix the issue in the file in a clone: contract, agent, red/green. Exit 1: failed, 2: never started",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			if o.Config, err = config.Load(p.Root, cmd.Flags()); err != nil {
				return err
			}
			if timeout > 0 {
				o.Config.Timeouts.Agent = timeout
			}
			o.Repo, o.Issue, o.Out = p.Root, args[0], cmd.OutOrStdout()
			return run.Run(o)
		},
	}
	cmd.Flags().StringVar(&o.Label, "label", "",
		"the run's directory under .atm/runs, which must not exist yet (default: a timestamp)")
	cmd.Flags().StringVar(&o.BaseRef, "base-ref", "main", "the commit the clone starts from")
	cmd.Flags().StringArrayVar(&o.Scope, "scope", nil,
		"a glob the diff may touch (repeatable; default: the paths the issue names)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "seconds before the agent is killed (default: timeouts.agent)")
	return cmd
}
