package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// installConfig is a complete .atm.yaml whose install is the line and every other key empty.
func installConfig(install string) string {
	return "install: " + install + "\ntest: ''\ntypecheck: ''\nlint: ''\ntest_file: ''\n" +
		"test_patterns: []\ndocs_patterns: []\ndelivery: ''\n"
}

// atm doctor flags an install the reach guard would reject if the worker ran it from its clone, and passes
// one that stays inside.
func TestDoctorFlagsInstallReachingOutsideTheClone(t *testing.T) {
	doctorRepoWithConfig(t, installConfig("cp /abs/outside/x .tmp/x"))
	out, err := atm(t, "doctor")
	fail := ""
	for _, l := range lines(t, out) {
		if strings.HasPrefix(l, "fail install: ") {
			fail = l
		}
	}
	if fail != "fail install: install reaches outside the clone: cp /abs/outside/x .tmp/x" {
		t.Fatalf("want the install line failing on its reach, got\n%s", out)
	}
	if run.ExitCode(err) == 0 {
		t.Fatal("want a non-zero exit")
	}
}

func TestDoctorTakesInstallInsideTheClone(t *testing.T) {
	doctorRepoWithConfig(t, installConfig("scripts/slopslint.sh --version"))
	out, err := atm(t, "doctor")
	if err != nil {
		t.Fatalf("atm doctor: %v\n%s", err, out)
	}
	ok := ""
	for _, l := range lines(t, out) {
		if strings.HasPrefix(l, "ok install: ") {
			ok = l
		}
	}
	if ok != "ok install: stays inside the clone" {
		t.Fatalf("want the install line ok, got\n%s", out)
	}
}
