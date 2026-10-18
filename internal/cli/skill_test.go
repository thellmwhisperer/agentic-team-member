package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestSkillPrintsRepositoryFile(t *testing.T) {
	out, err := atm(t, "skill")
	if err != nil {
		t.Fatalf("atm skill must succeed: %v", err)
	}
	want := read(t, "../../skills/atm/SKILL.md")
	if out != want {
		t.Fatalf("atm skill differs from skills/atm/SKILL.md: got %q", out)
	}
	if !strings.HasPrefix(strings.ReplaceAll(out, "\r\n", "\n"), "---\nname: atm\ndescription:") {
		t.Fatal("skill must have harness frontmatter")
	}
}

func TestSkillInstall(t *testing.T) {
	want, err := atm(t, "skill")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "skills")
	path := filepath.Join(dir, "atm", "SKILL.md")
	for range 2 {
		if out, err := atm(t, "skill", "install", "--dir", dir); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if got := read(t, path); got != want {
			t.Fatalf("installed bytes differ: %q", got)
		}
	}
	if err := os.WriteFile(path, []byte("local changes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = atm(t, "skill", "install", "--dir", dir)
	message := "skill at " + path + " differs from this atm; use --force"
	if run.ExitCode(err) != 2 || err == nil || err.Error() != message {
		t.Fatalf("want exit 2 and %q, got %v", message, err)
	}
	if got := read(t, path); got != "local changes\n" {
		t.Fatal("refusal changed the existing skill")
	}
	if out, err := atm(t, "skill", "install", "--dir", dir, "--force"); err != nil {
		t.Fatalf("force: %v\n%s", err, out)
	}
	if got := read(t, path); got != want {
		t.Fatal("force did not restore shipped bytes")
	}
}

func TestSkillDefaultInstallAndDoctor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	path := filepath.Join(home, ".claude", "skills", "atm", "SKILL.md")
	check := func(want string) {
		t.Helper()
		out, _ := atm(t, "doctor") // Other prerequisites need not be installed.
		var found []string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "skill:") {
				found = append(found, line)
			}
		}
		if len(found) != 1 || found[0] != want {
			t.Fatalf("want exactly %q, got\n%s", want, out)
		}
	}
	check("skill: missing")
	if out, err := atm(t, "skill", "install"); err != nil {
		t.Fatalf("default install: %v\n%s", err, out)
	}
	want, err := atm(t, "skill")
	if err != nil || read(t, path) != want {
		t.Fatalf("default installation differs: %v", err)
	}
	check("skill: ok")
	if err := os.WriteFile(path, []byte("old skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("skill: outdated (run atm skill install --force)")
}

func TestSkillInstallFollowsDirectorySymlinks(t *testing.T) {
	for _, component := range []string{"skills", "atm"} {
		t.Run(component, func(t *testing.T) {
			dir, target := t.TempDir(), t.TempDir()
			link := filepath.Join(dir, component)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(target, "SKILL.md")
			if component == "skills" {
				dir = link
				installed = filepath.Join(target, "atm", "SKILL.md")
			}
			if out, err := atm(t, "skill", "install", "--dir", dir); err != nil {
				t.Fatalf("symlink install: %v\n%s", err, out)
			}
			want, err := atm(t, "skill")
			if err != nil || read(t, installed) != want {
				t.Fatalf("symlink target differs: %v", err)
			}
		})
	}
}

func TestSkillCommandsMatchCLI(t *testing.T) {
	skill, err := atm(t, "skill")
	if err != nil {
		t.Fatal(err)
	}
	commands := regexp.MustCompile("`(atm [^`\n]+)`").FindAllStringSubmatch(skill, -1)
	for _, block := range regexp.MustCompile("(?s)```[^\n]*\n(.*?)```").FindAllStringSubmatch(skill, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "atm ") {
				commands = append(commands, []string{line, line})
			}
		}
	}
	if len(commands) == 0 {
		t.Fatal("skill names no commands")
	}
	for _, match := range commands {
		t.Run(match[1], func(t *testing.T) {
			args := strings.Fields(match[1])[1:]
			cmd, rest, err := root().Find(args)
			if err != nil || cmd.Name() == "atm" {
				t.Fatalf("unknown command %q: %v", match[1], err)
			}
			// run delegates parsing to the run package; check its live parser too.
			if cmd.Name() == "run" && !run.Follows(rest) {
				t.Fatalf("run does not accept %q as a followed run", rest)
			}
			cmd.DisableFlagParsing = false
			if err := cmd.ParseFlags(rest); err != nil {
				t.Fatalf("unknown flag in %q: %v", match[1], err)
			}
			if cmd.Args != nil {
				if err := cmd.Args(cmd, cmd.Flags().Args()); err != nil {
					t.Fatalf("invalid arguments in %q: %v", match[1], err)
				}
			}
		})
	}
}
