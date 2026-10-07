package cli

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// runCmd is atm run: the run goes to the repository's background process and, on a terminal, atm stays on its
// screen until it ends. Closing the terminal leaves it going.
func runCmd() *cobra.Command {
	return &cobra.Command{Use: "run", Short: "Fix an issue: " + run.Usage, DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			o, err := run.Start(root, args)
			if err != nil {
				return err
			}
			if f, ok := cmd.OutOrStdout().(*os.File); !ok || !terminal(f) {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), o.Run)
				return err
			}
			return attach(root, o.Run, cmd.OutOrStdout())
		}}
}

func terminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// attach shows run label's screen on w until it ends and returns how it ended.
func attach(root, label string, w io.Writer) error {
	o, err := run.Attach(root, label, w)
	if err != nil {
		return err
	}
	return o.Err()
}

func attachCmd() *cobra.Command {
	return &cobra.Command{Use: "attach [run]", Short: "Show a run's screen, the last running one by default; " +
		"leaving leaves it going", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			return attach(root, append(args, "")[0], cmd.OutOrStdout())
		}}
}

// listCmd is atm status, the runs going, when going, else atm runs, every run; one line each.
func listCmd(going bool) *cobra.Command {
	c := &cobra.Command{Use: "runs", Short: "List the repository's runs: running, passed and failed", Args: cobra.NoArgs}
	if going {
		c.Use, c.Short = "status", "Show what runs in the repository now"
	}
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		root, err := toplevel()
		if err != nil {
			return err
		}
		runs, err := run.Runs(root)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		n := 0
		for _, o := range runs {
			how := o.Outcome
			switch {
			case going && how != "running":
				continue
			case going:
				how = o.Step
			case o.FailedNode != "":
				how += " at " + o.FailedNode
			}
			n++
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.Run, how, o.Duration(), o.Issue)
		}
		if n == 0 {
			_, _ = fmt.Fprintln(w, map[bool]string{true: "nothing runs", false: "no run yet"}[going])
		}
		return w.Flush()
	}
	return c
}

// serveCmd is the repository's background process, which the first command that needs it starts.
func serveCmd() *cobra.Command {
	return &cobra.Command{Use: "serve", Short: "Be the repository's background process", Hidden: true,
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			return run.Serve(root)
		}}
}
