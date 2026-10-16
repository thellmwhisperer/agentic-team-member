package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	atmskill "github.com/thellmwhisperer/agentic-team-member/skills/atm"
)

func skillCmd() *cobra.Command {
	c := &cobra.Command{Use: "skill", Short: "Print ATM's driving skill", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), atmskill.Skill)
			return err
		}}
	var dir string
	var force bool
	install := &cobra.Command{Use: "install", Short: "Install ATM's driving skill", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("dir") {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".claude", "skills")
			}
			path := filepath.Join(dir, "atm", "SKILL.md")
			existing, err := os.ReadFile(path)
			switch {
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return err
			case err == nil && string(existing) == atmskill.Skill:
				return nil
			case err == nil && !force:
				return fmt.Errorf("skill at %s differs from this atm; use --force", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(atmskill.Skill), 0o644)
		}}
	install.Flags().StringVar(&dir, "dir", "", "skills directory (default ~/.claude/skills)")
	install.Flags().BoolVar(&force, "force", false, "overwrite a differing skill")
	c.AddCommand(install)
	return c
}

func skillStatus(home string) string {
	if home == "" {
		return "missing"
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "atm", "SKILL.md"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case err == nil && string(b) == atmskill.Skill:
		return "ok"
	default:
		return "outdated (run atm skill install --force)"
	}
}
