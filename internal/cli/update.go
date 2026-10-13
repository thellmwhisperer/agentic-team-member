package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/buildinfo"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func updateCmd() *cobra.Command {
	return &cobra.Command{Use: "update", Short: "Replace this atm with the latest published release", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err == nil {
				exe, err = filepath.EvalSymlinks(exe)
			}
			if err != nil {
				return err
			}
			return update(cmd.OutOrStdout(), buildinfo.CurrentVersion(), exe)
		}}
}

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// update replaces the atm at target, which is current, with the latest release; not while a run of the repository
// goes, and not over a build that is no release.
func update(w io.Writer, current, target string) error {
	if root, err := toplevel(); err == nil {
		runs, err := run.Runs(root)
		if err != nil {
			return err
		}
		var going []string
		for _, o := range runs {
			if o.Outcome == "running" {
				going = append(going, o.Run)
			}
		}
		if len(going) > 0 {
			return fmt.Errorf("runs going here: %s; update once they end", strings.Join(going, ", "))
		}
	}
	latest, err := release("latest")
	if err != nil {
		return err
	}
	if latest.Tag == current {
		_, err = fmt.Fprintf(w, "atm %s is the latest release\n", current)
		return err
	}
	if !releaseTag.MatchString(current) {
		return fmt.Errorf("this atm is %s, a build and not a release, and the latest release is %s: install it with "+
			"install.sh", current, latest.Tag)
	}
	bin, err := latest.binary()
	if err == nil {
		err = swap(target, bin, latest.Tag)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "atm %s -> %s\n", current, latest.Tag)
	return err
}
