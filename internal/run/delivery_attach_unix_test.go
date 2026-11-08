//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRealRunRefusesNoMistakesAttachBeforeClone(t *testing.T) {
	bin := builtATM(t)
	// The issue's delivery fixture, verbatim. It must never execute.
	const delivery = `delivery: >-
  no-mistakes init >/dev/null &&
  { no-mistakes axi run --intent "$ATM_TITLE" --closes "$ATM_ISSUE" ` +
		`--base-branch integration --wait 2h >/dev/null 2>&1 & } &&
  until no-mistakes axi status 2>/dev/null | grep -q '^  status: running'; do sleep 1; done &&
  { no-mistakes attach || true; }
`
	for _, follow := range []bool{false, true} {
		name := "run"
		if follow {
			name = "run --follow"
		}
		t.Run(name, func(t *testing.T) {
			config := strings.Replace(atmSet(atmYAML, "install", "exit 37"), "delivery: \"\"\n", delivery, 1)
			root := repo(t, "local", config)
			stopServing(t, root)
			tools := t.TempDir()
			marker := filepath.Join(tools, "executed")
			writeFixture(t, filepath.Join(tools, "no-mistakes"), "#!/bin/sh\ntouch '"+marker+"'\nexit 99\n", 0o755)
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			args := []string{issueFile(t, issue)}
			if follow {
				args = append([]string{"--follow"}, args...)
			}
			out, code := preflightLaunch(t, bin, root, args...)
			assertStopped(t, root, out, code, "config", ".atm.yaml: delivery")
			for _, want := range []string{"no-mistakes attach", "replace", "no-mistakes axi run", "ATM paints"} {
				if !strings.Contains(out, want) {
					t.Errorf("refusal lacks %q:\n%s", want, out)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("delivery executed before refusal: %v", err)
			}
			for _, path := range []string{".atm/runs", ".atm/clones"} {
				if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
					t.Errorf("refused run created %s: %v", path, err)
				}
			}
		})
	}
}

func TestRealRunChecksDeliveryCommands(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []struct {
		name, delivery string
		refused        bool
	}{
		{"quoted command", `'no-mistakes' "attach"`, true},
		{"path and whitespace", "/tools/no-mistakes \\\n\tattach --run example", true},
		{"command substitution", `printf '%s' "$(no-mistakes attach)"`, true},
		{"launch only", `no-mistakes axi run --intent "$ATM_TITLE"`, false},
		{"argument", `printf '%s\n' 'no-mistakes attach'`, false},
		{"comment", "true # no-mistakes attach", false},
		{"heredoc", "cat <<'EOF'\nno-mistakes attach\nEOF\n", false},
		{"shell syntax stays with delivery", "true && (", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := strings.Replace(atmYAML, `delivery: ""`, "delivery: "+strconv.Quote(c.delivery), 1)
			root := repo(t, "local", config)
			// The worker-file check proves config passed without starting a run or executing delivery.
			out, code := preflightLaunch(t, bin, root, "--harness-arg", "--settings=missing.json", issueFile(t, issue))
			check, why := "worker files", "--settings:"
			if c.refused {
				check, why = "config", ".atm.yaml: delivery: no-mistakes attach"
			}
			assertStopped(t, root, out, code, check, why)
		})
	}
}
