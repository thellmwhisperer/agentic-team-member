//go:build unix

package run

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRunAllowsDeletedOrShorterBaseTests: the guard on base tests is retired (decision 7987). The real atm
// delivers a feature whose worker deletes a_test.sh, a test on the base, or shortens it, when the configured
// suite passes; a configured suite that fails still stops it at Checks, before delivery.
func TestRunAllowsDeletedOrShorterBaseTests(t *testing.T) {
	for _, c := range []struct {
		name, work       string
		kept, suiteFails bool
	}{
		{name: "deleted", work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh")},
		{name: "shorter", work: "echo fixed > a.txt; : > a_test.sh" + reportTest("regression_test.sh"),
			kept: true},
		{name: "deleted with a failing suite", suiteFails: true,
			work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh")},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := builtATM(t)
			fails, delivered := filepath.Join(t.TempDir(), "fails"), filepath.Join(t.TempDir(), "delivered")
			t.Setenv("ATM_TEST_SUITE_FAILS", fails)
			t.Setenv("ATM_TEST_OUT", delivered)
			t.Setenv("FAKE_AGENT_WORK", c.work)
			if c.suiteFails {
				writeFixture(t, fails, "")
			}
			cfg := atmSet(atmSet(atmYAML, "test", `test ! -f "$ATM_TEST_SUITE_FAILS" && `+suite),
				"delivery", `ls > "$ATM_TEST_OUT"`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
				t.Fatal(err)
			}
			server := startVersionServe(t, bin, top)
			t.Cleanup(func() { endServe(bin, top, server) })
			issue := issueFile(t, "# Retry\nType: feature\nRetry once.\n")
			if c.suiteFails {
				followedRun(t, bin, top, 1, issue)
				rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, "failed")), "report.json"))
				if rep["failed_node"] != "checks" || !strings.HasPrefix(rep["reason"].(string), "test: ") {
					t.Fatalf("want the configured suite to stop Checks, got %v", rep)
				}
				if _, err := os.Stat(delivered); !os.IsNotExist(err) {
					t.Fatalf("a failing suite delivered: %v", err)
				}
				return
			}
			followedRun(t, bin, top, 0, issue)
			lastRun(t, top, "passed")
			b, err := os.ReadFile(delivered)
			if err != nil {
				t.Fatalf("nothing delivered: %v", err)
			}
			files := strings.Fields(string(b))
			if !slices.Contains(files, "regression_test.sh") || slices.Contains(files, "a_test.sh") != c.kept {
				t.Fatalf("want the %s base test delivered as the worker left it, got %v", c.name, files)
			}
		})
	}
}

func TestRunPonytailAllowsDeletedOrShorterTests(t *testing.T) {
	const redundant = "true\ntrue # required\ntrue\n"
	const proven = "grep -q fixed a.txt && ! grep -q broken a.txt\n"
	_, source, _, _ := runtime.Caller(0)
	t.Setenv("ATM_TEST_SLOPSLINT", filepath.Join(filepath.Dir(source), "../../scripts/slopslint.sh"))
	for _, action := range []struct{ name, work, want string }{
		{"deleted", "rm redundant_test.sh", "deleted\n"},
		{"shorter", "echo true > redundant_test.sh", "true\n"},
	} {
		for _, mode := range []string{"passing checks", "failing suite", "changed proof"} {
			t.Run(action.name+"/"+mode, func(t *testing.T) {
				builtATM(t)
				delivered := filepath.Join(t.TempDir(), "delivered")
				t.Setenv("ATM_TEST_OUT", delivered)
				t.Setenv("FAKE_AGENT_WORK", fixWork+"; echo true >> redundant_test.sh")
				work := action.work
				if mode == "changed proof" {
					work += "; echo true > a_test.sh"
				}
				t.Setenv("FAKE_PONYTAIL_WORK", work)
				t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("redundant_test.sh"))
				check := suite
				if mode == "failing suite" {
					check = "grep -q required redundant_test.sh && " + check
				}
				cfg := atmSet(atmSet(atmYAML, "test", check), "delivery",
					`{ if git cat-file -e HEAD:redundant_test.sh 2>/dev/null; then `+
						`git show HEAD:redundant_test.sh; else echo deleted; fi; `+
						`git show HEAD:a_test.sh; git log -1 --format=%s; } > "$ATM_TEST_OUT"; `+
						`if [ -d .slop/tombstones ]; then `+
						`cat .slop/tombstones/*.yml > "$ATM_TEST_OUT.tombstone"; `+
						`bash "$ATM_TEST_SLOPSLINT" tombstone check --repo-root "$PWD"; fi`)
				top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
				writeFixture(t, filepath.Join(top, "redundant_test.sh"), "true\ntrue # required\n")
				gitT(t, top, "add", "redundant_test.sh")
				gitT(t, top, "commit", "--amend", "--no-edit", "-q")
				realPonytailConfig(t, top, source)
				bin := followUpsServer(t, top)
				followedRun(t, bin, top, 0, issueFile(t, issue))
				n := reportNode(t, readReport(t, filepath.Join(runDir(top, lastRun(t, top, "passed")), "report.json")),
					"ponytail")
				want := action.want + proven + "ponytail: 1 cuts\n"
				rejected, reason := "", ""
				switch mode {
				case "failing suite":
					want = redundant + proven + "fix: Retry on timeout\n"
					rejected, reason = "redundant_test.sh", "test: "
				case "changed proof":
					rejected, reason = "a_test.sh", "unit 1: test a_test.sh changed since it was proven"
				}
				if n["kept"] != (mode != "failing suite") {
					t.Fatalf("unexpected Ponytail decision: %v", n)
				}
				files := n["rejected_files"].([]any)
				if rejected == "" && len(files) != 0 || rejected != "" && (len(files) != 1 ||
					files[0].(obj)["path"] != rejected || !strings.Contains(files[0].(obj)["reason"].(string), reason)) {
					t.Fatalf("want rejection of %q for %q, got %v", rejected, reason, files)
				}
				if b, err := os.ReadFile(delivered); err != nil || string(b) != want {
					t.Fatalf("want delivery %q, got %q, %v", want, b, err)
				}
				if mode == "failing suite" {
					return
				}
				var tomb struct {
					ID       string
					Incident struct {
						Evidence []struct{ Example, Artifact string }
					}
					Match struct{ Artifact string }
				}
				b, err := os.ReadFile(delivered + ".tombstone")
				if err != nil || yaml.Unmarshal(b, &tomb) != nil {
					t.Fatalf("cannot read delivered tombstone: %s, %v", b, err)
				}
				example, artifact := "cut redundant_test.sh\nissue_sentence: Retry once.", "redundant_test.sh"
				if action.name == "deleted" {
					example += "\nDeleted redundant_test.sh:\n" + redundant
					artifact = ".slop/tombstones/" + tomb.ID + ".yml"
				}
				if len(tomb.Incident.Evidence) != 1 || tomb.Incident.Evidence[0].Example != example ||
					tomb.Incident.Evidence[0].Artifact != artifact || tomb.Match.Artifact != artifact {
					t.Fatalf("want the cut's evidence retained in a valid artifact, got %+v", tomb)
				}
			})
		}
	}
}
