package cli

import (
	"os"
	"path/filepath"
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
	root := doctorRepoWithConfig(t, installConfig(""))
	top, err := toplevel()
	if err != nil {
		t.Fatal(err)
	}
	install := "cp " + filepath.ToSlash(top) + "/.tmp/slopslint/x .tmp/x"
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(installConfig(install)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := atm(t, "doctor")
	fail := ""
	for _, l := range lines(t, out) {
		if strings.HasPrefix(l, "fail install: ") {
			fail = l
		}
	}
	if fail != "fail install: install reaches outside the clone: "+install {
		t.Fatalf("want the install line failing on its reach, got\n%s", out)
	}
	if run.ExitCode(err) == 0 {
		t.Fatal("want a non-zero exit")
	}
}

func TestDoctorTakesInstallInsideTheClone(t *testing.T) {
	for _, install := range []string{"scripts/slopslint.sh --version", "/usr/local/go/bin/go mod download"} {
		t.Run(install, func(t *testing.T) {
			doctorRepoWithConfig(t, installConfig(install))
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
		})
	}
}
