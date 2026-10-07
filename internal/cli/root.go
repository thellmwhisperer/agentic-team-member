// Package cli is atm's command line: one cobra command per file, run from inside the repository it works on.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// Execute runs atm with os.Args and exits with its exit code.
func Execute() {
	err := root().Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "atm:", err)
	}
	os.Exit(run.ExitCode(err))
}

// root is atm with every command; it prints errors to no one, Execute does.
func root() *cobra.Command {
	c := &cobra.Command{Use: "atm", Short: "ATM fixes an issue in the repository it runs in",
		SilenceErrors: true, SilenceUsage: true}
	c.AddCommand(runCmd(), initCmd(), doctorCmd())
	return c
}

// toplevel is the root of the git repository atm runs in.
func toplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
