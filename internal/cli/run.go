package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
	"github.com/thellmwhisperer/agentic-team-member/internal/tui"
)

// runCmd hands the run to the repository's background process, so closing the terminal leaves it going.
// Without --follow, a terminal shows the screen until the run ends, or after passed Delivery until the gate ends,
// unless the user closes it first.
func runCmd() *cobra.Command {
	c := &cobra.Command{Use: "run", Short: "Fix an issue: " + run.Usage,
		Long: "Without --follow, on a terminal the screen shows until the run ends or q or Ctrl+C.\n" +
			"With --follow, print each run event as JSON until the run ends. Exit with the run's code.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			if run.Follows(args) {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return run.StartFollowing(ctx, root, args, cmd.OutOrStdout())
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
	// Publish the skill's flag in cobra too; run still owns argument parsing.
	c.Flags().Bool("follow", false, "print the run's events as JSON until it ends")
	return c
}

func terminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// screen says whether w gets the run's screen rather than its plain lines: a terminal, unless NO_COLOR or
// TERM=dumb asks for plain lines.
func screen(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	return f, ok && terminal(f) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// attach shows run label's screen on w until the run ends, or after passed Delivery until the gate ends,
// unless the user closes it first, and returns how the run ended.
// Leaving leaves it going.
func attach(root, label string, w io.Writer) error {
	f, ok := screen(w)
	if !ok {
		o, err := tui.Attach(root, label, w)
		if err != nil {
			return err
		}
		return o.Err()
	}
	o, detached, err := tui.Show(root, label, os.Stdin, f)
	switch {
	case err != nil:
		return err
	case detached:
		_, err = fmt.Fprintln(w, "the run goes on:", strings.TrimSpace("atm attach "+label))
		return err
	}
	return o.Err()
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach [run]",
		Short: "Show a run's screen until it ends or q or Ctrl+C",
		Long:  "The last running run is selected by default. Leaving a live screen leaves the run going.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			return attach(root, append(args, "")[0], cmd.OutOrStdout())
		}}
}

// listCmd is atm status, the runs going, when going, else atm runs, every run and its report.json; one line each.
func listCmd(going bool) *cobra.Command {
	c := &cobra.Command{Use: "runs", Short: "List the repository's runs: running, passed and failed", Args: cobra.NoArgs}
	if going {
		c.Use, c.Short = "status", "Show what runs in the repository now"
	}
	asJSON := c.Flags().Bool("json", false, "print JSON")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		root, err := toplevel()
		if err != nil {
			return err
		}
		runs, err := run.Runs(root)
		if err != nil {
			return err
		}
		if *asJSON {
			return list(cmd.OutOrStdout(), true, going, runs)
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
			report := ""
			if !going && o.Report != "" {
				report = "\t" + o.Report
			}
			serve := ""
			if !going {
				serve = "\t" + o.ServeLabel()
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s%s%s\n",
				o.Run, how, o.Duration(), o.AgentLabel(), o.Issue, report, serve)
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
