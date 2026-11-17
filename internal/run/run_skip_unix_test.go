//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type skipCase struct {
	name, config string
	skip         []string
	ponytail     string // the ponytail node's reason, "" when it runs
	delivery     string // the delivery node's reason, "" when it runs
}

// atm run --skip ponytail|delivery skips that step for this run only, whatever .atm.yaml says.
func TestRunSkipFlag(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []skipCase{
		{"ponytail", "", []string{"ponytail"}, "skipped by flag: --skip ponytail", ""},
		{"ponytail over config false", "ponytail: false\n", []string{"ponytail"}, "skipped by flag: --skip ponytail", ""},
		{"delivery", "", []string{"delivery"}, "", "skipped by flag: --skip delivery"},
		{"both, repeated", "", []string{"delivery", "ponytail"}, "skipped by flag: --skip ponytail", "skipped by flag: --skip delivery"},
		{"no flag", "", nil, "", ""},
		{"config false, no flag", "ponytail: false\n", nil, "skipped by configuration: ponytail: false", ""},
	} {
		t.Run(c.name, func(t *testing.T) { skipRun(t, bin, c) })
	}
	t.Run("delivery after checks stop", func(t *testing.T) { skipDeliveryAfterChecksStop(t, bin) })
}

func skipDeliveryAfterChecksStop(t *testing.T, bin string) {
	t.Helper()
	cfg := atmSet(atmSet(atmYAML, "typecheck", "echo failed-check-finding; exit 1"),
		"delivery", "no-such-command-for-atm delivered")
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	ponytailConfigServer(t, bin, top)
	live := leftAtDecision(t, startFollow(t, bin, top, true, "run", "--skip", "delivery", issueFile(t, issue)))
	assertDeliverySkippedOnScreen(t, live)
	cmd := exec.Command(bin, "answer", "issue-1", "stop", "--by", "operator")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop after failed Checks: %v\n%s", err, out)
	}
	reopened := decisionTerminal(t, startFollow(t, bin, top, true, "attach", "issue-1"), 1)
	assertDeliverySkippedOnScreen(t, reopened)
	assertSkipNode(t, e2eReport(t, top, "issue-1").node("delivery"), "skipped by flag: --skip delivery")
}

func assertDeliverySkippedOnScreen(t *testing.T, screen string) {
	t.Helper()
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "Delivery") && strings.Contains(line, "skipped") {
			return
		}
	}
	t.Fatalf("Delivery was not shown as skipped:\n%s", screen)
}

func skipRun(t *testing.T, bin string, c skipCase) {
	t.Helper()
	// A run that skips delivery passes its preflight although the first word of its delivery is no command.
	delivery := "printf delivered > delivered"
	if c.delivery != "" {
		delivery = "no-such-command-for-atm delivered"
	}
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", delivery)+c.config), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	ponytailConfigServer(t, bin, top) // a serve that ends by itself writes its coverage counters, a killed one does not
	args := []string{"run", "--follow"}
	for _, s := range c.skip {
		args = append(args, "--skip", s)
	}
	startFollow(t, bin, top, false, append(args, issueFile(t, issue))...).exit(0)
	r := e2eReport(t, top, "issue-1")
	assertSkipNode(t, r.node("ponytail"), c.ponytail)
	assertSkipNode(t, r.node("delivery"), c.delivery)
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	dir := runDir(top, "issue-1")
	if exists(filepath.Join(dir, "brief-ponytail.md")) != (c.ponytail == "") {
		t.Errorf("ponytail brief written with reason %q", c.ponytail)
	}
	if exists(filepath.Join(dir, "delivery-output.txt")) != (c.delivery == "") {
		t.Errorf("delivery ran with reason %q", c.delivery)
	}
	plain := exec.Command(bin, "attach", "issue-1")
	plain.Dir = top
	plain.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := plain.CombinedOutput()
	if err != nil {
		t.Fatalf("attach: %s, %v", out, err)
	}
	if got := strings.Contains(strings.ToLower(string(out)), "slop detector"); got != (c.ponytail == "") {
		t.Errorf("slop detector on screen = %v with reason %q:\n%s", got, c.ponytail, out)
	}
}

// assertSkipNode checks that n is skipped with reason, or passed when reason is empty.
func assertSkipNode(t *testing.T, n *node, reason string) {
	t.Helper()
	if reason != "" {
		if n.Result != "skipped" || n.Reason == nil || *n.Reason != reason || n.StartedAt != "" {
			t.Errorf("%s node = %+v, want skipped with %q", n.Name, n, reason)
		}
	} else if n.Result != "passed" {
		t.Errorf("%s node = %+v, want passed", n.Name, n)
	}
}

// --skip with any other step fails before the run starts, and lists the steps it accepts.
func TestRunSkipFlagUnknownStep(t *testing.T) {
	bin := builtATM(t)
	for _, step := range []string{"agent", "checks", "nonsense", ""} {
		t.Run(step, func(t *testing.T) {
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			stopServing(t, top)
			cmd := exec.Command(bin, "run", "--skip", step, issueFile(t, issue))
			cmd.Dir = top
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "delivery, ponytail") {
				t.Fatalf("atm run --skip %q = %s, %v; want an error listing delivery, ponytail", step, out, err)
			}
			if _, err := os.Stat(runDir(top, "issue-1")); !os.IsNotExist(err) {
				t.Errorf("the run started: %v", err)
			}
		})
	}
}

// Without --skip delivery, a delivery whose first word is no command fails preflight before the run starts.
func TestRunSkipFlagDeliveryNeedsItsCommand(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", "no-such-command-for-atm delivered")),
		"rev-parse", "--show-toplevel")
	stopServing(t, top)
	cmd := exec.Command(bin, "run", issueFile(t, issue))
	cmd.Dir = top
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "preflight: delivery: ") ||
		!strings.Contains(string(out), "is not on PATH nor a word of sh") {
		t.Fatalf("atm run = %s, %v; want the delivery preflight error", out, err)
	}
	if _, err := os.Stat(runDir(top, "issue-1")); !os.IsNotExist(err) {
		t.Errorf("the run started: %v", err)
	}
}
