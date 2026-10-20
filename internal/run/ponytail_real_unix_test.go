//go:build unix

package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// realCut is a real run whose ponytail agent cuts with work and reports report, under lint; delivered is what
// its delivery sees of the ponytail commit, "" when the cut is undone, and rejected the files report.json names.
type realCut struct {
	name, work, report, lint, delivered string
	rejected                            []string
}

// A real run keeps a ponytail cut that quotes a sentence of the issue and passes the proof and the checks, and
// judges the files of a cut together: what a failure names, then each file's own removal, is undone.
// The unit adds x.go and y.go, which only build together, and z.txt beside a.txt's fix.
func TestRealPonytailKeepsACutThatQuotesTheIssue(t *testing.T) {
	builtATM(t) // before repo moves HOME, where go build would fill a module cache
	const (
		names = `! grep -q broken z.txt || { echo "z.txt:1: broken"; exit 1; }`
		xyz   = dependentCut + "; echo broken > z.txt"
	)
	all := dependentCuts("x.go", "y.go", "z.txt")
	for _, c := range []realCut{
		{name: "quotes the issue", work: "echo fixed > a.txt",
			report:    strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1),
			delivered: "fixed\nponytail: 1 cuts\n"},
		{name: "quoted YAML characters", work: "echo fixed > a.txt",
			report: strings.Replace(cut, `"issue_sentence": ""`,
				`"issue_sentence": "Retry \"once\".\npath: C:\\tmp\t# quote"`, 1),
			delivered: "fixed\nponytail: 1 cuts\n"},
		{name: "dependent pair", work: dependentCut, report: dependentCuts("x.go", "y.go"),
			delivered: "fixed\nextra\nmore\nponytail: 2 cuts\nx.go\ny.go\n"},
		{name: "a check names z.txt", work: xyz, report: all, lint: names,
			delivered: "fixed\nextra\nmore\nponytail: 2 cuts\nx.go\ny.go\n", rejected: []string{"z.txt"}},
		{name: "a check names no cut file", work: xyz, report: all,
			lint:      `! grep -q broken z.txt || { echo "a line is broken"; exit 1; }`,
			delivered: "fixed\nextra\nmore\nponytail: 2 cuts\nx.go\ny.go\n", rejected: []string{"z.txt"}},
		{name: "no removal holds", work: xyz, report: all,
			lint:     `grep -q unused x.go || { echo "a line is broken"; exit 1; }`,
			rejected: []string{"x.go", "y.go", "z.txt"}},
		{name: "the only cut fails the proof", work: "echo broken > a.txt", report: cut, rejected: []string{"a.txt"}},
		{name: "a check names the only cut", work: "echo broken > z.txt", report: dependentCuts("z.txt"), lint: names,
			rejected: []string{"z.txt"}},
		{name: "no issue_sentence", work: "echo fixed > a.txt",
			report: strings.Replace(cut, `, "issue_sentence": ""`, "", 1), rejected: []string{"a.txt"}},
	} {
		t.Run(c.name, func(t *testing.T) { realPonytail(t, c) })
	}
	for _, diagnostic := range []string{
		"dryrun.go:1: broken", "run.go.bak:1: broken", "d/run.go:1: broken",
		`d\run.go:1: broken`, "./d/run.go:1: broken",
	} {
		bad := strings.TrimPrefix(strings.ReplaceAll(strings.Split(diagnostic, ":")[0], `\`, "/"), "./")
		t.Run(diagnostic, func(t *testing.T) {
			realPonytail(t, realCut{work: "echo v2 > run.go; echo broken > " + bad,
				report: dependentCuts("run.go", bad), rejected: []string{bad},
				lint: `! grep -q broken ` + bad + ` || { printf "%s\n" "` +
					strings.ReplaceAll(diagnostic, `\`, `\\`) + `"; exit 1; }`,
				delivered: "fixed\nextra\nmore\nponytail: 1 cuts\nrun.go\n"})
		})
	}
}

// realPonytail runs c through the real atm binary and its own serve, which it ends gracefully, and checks the
// run passed, report.json's ponytail decision and what the delivery saw.
func realPonytail(t *testing.T, c realCut) {
	const builds = "go test x.go y.go"
	delivered := filepath.Join(t.TempDir(), "delivered")
	_, source, _, _ := runtime.Caller(0)
	t.Setenv("ATM_TEST_SLOPSLINT", filepath.Join(filepath.Dir(source), "../../scripts/slopslint.sh"))
	for k, v := range map[string]string{"ATM_TEST_OUT": delivered, "FAKE_PONYTAIL_WORK": c.work,
		"FAKE_AGENT_WORK": "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest +
			"; " + dependentSeed + "; mkdir -p d; for f in run.go dryrun.go run.go.bak d/run.go; " +
			"do printf 'v1\\nextra\\n' > $f; done",
		"FAKE_PONYTAIL_REPORT": c.report} {
		t.Setenv(k, v)
	}
	atm := atmSet(atmSet(atmSet(atmYAML, "typecheck", builds), "lint", c.lint), "delivery",
		`{ git show HEAD:a.txt; git log -1 --format=%s; `+
			`git diff --name-only HEAD~1 HEAD -- x.go y.go z.txt run.go dryrun.go run.go.bak d/run.go; }`+
			` > "$ATM_TEST_OUT"; mkdir -p "$ATM_TEST_OUT.tombs"; `+
			`if [ -d .slop/tombstones ]; then cp .slop/tombstones/*.yml "$ATM_TEST_OUT.tombs/" && `+
			`bash "$ATM_TEST_SLOPSLINT" tombstone check --repo-root "$PWD"; fi`)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atm), "rev-parse", "--show-toplevel")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	realPonytailConfig(t, top, source)
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test") // its own file, for endServe to change
	server := startVersionServe(t, bin, top)
	// A graceful exit flushes the real binary's coverage counters.
	defer endServe(bin, top, server)
	startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue)).exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 {
		t.Fatalf("want one run, got %+v, %v", runs, err)
	}
	n := reportNode(t, readReport(t, filepath.Join(top, ".atm", "runs", runs[0].Run, "report.json")), "ponytail")
	if n["kept"] != (c.delivered != "") || runs[0].Outcome != "passed" {
		t.Fatalf("want the run passed with the cut kept %t, got %s, %v", c.delivered != "", runs[0].Outcome, n)
	}
	var rejected []string
	for _, f := range n["rejected_files"].([]any) {
		rejected = append(rejected, f.(map[string]any)["path"].(string))
	}
	if !reflect.DeepEqual(rejected, c.rejected) {
		t.Fatalf("want rejected_files %v, got %v", c.rejected, n["rejected_files"])
	}
	b, err := os.ReadFile(delivered)
	if err != nil || c.delivered != "" && string(b) != c.delivered ||
		c.delivered == "" && strings.Contains(string(b), "ponytail:") {
		t.Fatalf("want the ponytail commit to hold the cut kept, delivered %q, %v", b, err)
	}
	realPonytailTombstones(t, c, delivered)
	t.Logf("atm run %s: ponytail kept=%v, rejected %v", runs[0].Run, n["kept"], rejected)
}

func realPonytailConfig(t *testing.T, top, source string) {
	t.Helper()
	config, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../.slop/config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(top, ".slop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, ".slop/config.yml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, top, "add", ".slop/config.yml")
	gitT(t, top, "commit", "--amend", "--no-edit", "-q")
}

func realPonytailTombstones(t *testing.T, c realCut, delivered string) {
	t.Helper()
	var report struct{ Findings []finding }
	if err := json.Unmarshal([]byte(c.report), &report); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(delivered + ".tombs/*.yml")
	var examples []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		var tomb struct {
			Incident struct{ Evidence []struct{ Example string } }
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, &tomb); err != nil {
			t.Fatal(err)
		}
		for _, evidence := range tomb.Incident.Evidence {
			examples = append(examples, evidence.Example)
		}
	}
	for _, f := range report.Findings {
		if c.delivered == "" || slices.Contains(c.rejected, f.File) {
			continue
		}
		want := f.Finding
		if f.IssueSentence != nil && *f.IssueSentence != "" {
			want += "\nissue_sentence: " + *f.IssueSentence
		}
		if !slices.Contains(examples, want) {
			t.Fatalf("tombstones lost evidence %q: %v", want, examples)
		}
	}
}
