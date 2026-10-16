package cli

import (
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/plan"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// axiCmd is atm for agents: atm axi run waits for the outcome; every command prints TOON, or JSON with --json.
func axiCmd() *cobra.Command {
	c := &cobra.Command{Use: "axi", Short: "atm for agents: TOON out, JSON with --json"}
	c.AddCommand(&cobra.Command{Use: "run [--json] " + strings.TrimPrefix(run.Usage, "usage: atm run "),
		Short: "Run an issue and print its outcome", DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args, asJSON := axiRunArgs(args)
			root, err := toplevel()
			if err != nil {
				return err
			}
			o, err := run.Start(root, args)
			if err == nil {
				o, err = run.Attach(root, o.Run, io.Discard)
			}
			if err != nil {
				return err
			}
			if err := outcome(cmd.OutOrStdout(), asJSON, o); err != nil {
				return err
			}
			return o.Err()
		}}, axiList(true), axiList(false), axiPlan())
	return c
}

// axiPlan is atm axi plan, which tickets of a CoS's plan can launch now; with --follow, until none is open.
func axiPlan() *cobra.Command {
	c := &cobra.Command{Use: "plan [--json] [--follow] <plan.json>", Short: "Print which tickets of a plan can launch",
		Args: cobra.ExactArgs(1)}
	asJSON := c.Flags().Bool("json", false, "print JSON")
	follow := c.Flags().Bool("follow", false, "print a JSON line each time the plan changes, until no ticket is open")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		root, err := toplevel()
		if err != nil {
			return err
		}
		p, err := plan.Load(args[0])
		if err != nil {
			return err
		}
		issue := plan.GitHub(run.GitHubRepo())
		if *follow {
			return plan.Follow(args[0], root, issue, cmd.OutOrStdout(), cmd.ErrOrStderr(), time.Now, time.Sleep)
		}
		s, err := plan.Read(p, root, issue)
		if err != nil {
			return err
		}
		var b strings.Builder
		if *asJSON {
			line, _ := json.Marshal(s)
			b.Write(append(line, '\n'))
		} else {
			for _, l := range []struct {
				key    string
				values []int
			}{{"open", s.Open}, {"ready", s.Ready}, {"cos_holds", s.CosHolds}, {"javi_holds", s.JaviHolds},
				{"working", s.Working}} {
				numbers := make([]string, len(l.values))
				for i, n := range l.values {
					numbers[i] = strconv.Itoa(n)
				}
				b.WriteString(strings.TrimSpace(l.key+"["+strconv.Itoa(len(l.values))+"]: "+strings.Join(numbers, ",")) + "\n")
			}
			b.WriteString("free: " + strconv.Itoa(s.Free) + "\n")
		}
		_, err = io.WriteString(cmd.OutOrStdout(), b.String())
		return err
	}
	return c
}

func axiRunArgs(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	asJSON, harnessArg := false, false
	for _, arg := range args {
		if harnessArg {
			filtered = append(filtered, arg)
			harnessArg = false
			continue
		}
		if arg == "--json" {
			asJSON = true
			continue
		}
		filtered = append(filtered, arg)
		harnessArg = arg == "--harness-arg"
	}
	return filtered, asJSON
}

// axiList is atm axi status, the runs going, when going, else atm axi runs, every run.
func axiList(going bool) *cobra.Command {
	c := &cobra.Command{Use: "runs", Short: "Print every run", Args: cobra.NoArgs}
	if going {
		c.Use, c.Short = "status", "Print the runs going"
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
		return list(cmd.OutOrStdout(), *asJSON, going, runs)
	}
	return c
}

// outcome prints how run o ended, every field even when empty.
func outcome(w io.Writer, asJSON bool, o run.Outcome) error {
	keys := []string{"outcome", "run", "failed_node", "reason", "report", "next_step"}
	values := []string{o.Outcome, o.Run, o.FailedNode, o.Reason, o.Report, o.NextStep}
	var b strings.Builder
	if asJSON {
		b.WriteString(jsonObject(keys, values) + "\n")
	} else {
		for i, k := range keys {
			b.WriteString(k + ": " + toon(values[i]) + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// list prints runs as a table, runs: the runs going, when going, else every run.
func list(w io.Writer, asJSON, going bool, runs []run.Outcome) error {
	keys := []string{"run", "issue", "outcome", "failed_node", "duration", "report", "agent"}
	if going {
		keys = []string{"run", "issue", "step", "duration", "agent"}
	}
	var rows [][]string
	for _, o := range runs {
		switch {
		case going && o.Outcome == "running":
			rows = append(rows, []string{o.Run, o.Issue, o.Step, o.Duration(), o.AgentLabel()})
		case !going:
			rows = append(rows, []string{o.Run, o.Issue, o.Outcome, o.FailedNode, o.Duration(), o.Report, o.AgentLabel()})
		}
	}
	var b strings.Builder
	if asJSON {
		objects := make([]string, len(rows))
		for i, row := range rows {
			objects[i] = jsonObject(keys, row)
		}
		b.WriteString(`{"runs":[` + strings.Join(objects, ",") + "]}\n")
	} else {
		b.WriteString("runs[" + strconv.Itoa(len(rows)) + "]{" + strings.Join(keys, ",") + "}:\n")
		for _, row := range rows {
			for i, v := range row {
				row[i] = toon(v)
			}
			b.WriteString("  " + strings.Join(row, ",") + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// jsonObject is the JSON object of keys and their values, strings all, in order.
func jsonObject(keys, values []string) string {
	fields := make([]string, len(keys))
	for i, k := range keys {
		key, _ := json.Marshal(k)
		value, _ := json.Marshal(values[i])
		fields[i] = string(key) + ":" + string(value)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

var (
	number = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][+-]?\d+)?$`)
	escape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
)

// toon is s as a TOON string: bare, unless TOON would read it as another value or across a delimiter.
func toon(s string) string {
	if s == "" || s != strings.TrimSpace(s) || s == "true" || s == "false" || s == "null" || number.MatchString(s) ||
		strings.HasPrefix(s, "-") || strings.ContainsAny(s, ":\"\\[]{},\n\r\t") {
		return `"` + escape.Replace(s) + `"`
	}
	return s
}
