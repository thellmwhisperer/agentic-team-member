package scripts

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	oldHead = "1111111111111111111111111111111111111111"
	newHead = "2222222222222222222222222222222222222222"
	review  = "scripts/no-mistakes-review.sh 7"
	arm     = "scripts/arm-auto-merge.sh 7"
	lowRisk = "## Risk Assessment\nLow\n"
	high    = "## Risk Assessment\nHigh\n"
)

// fakes holds this test binary copied as gh.
var fakes string

func TestMain(m *testing.M) {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "gh" {
		os.Exit(fakeGH(os.Args[1:]))
	}
	dir, err := os.MkdirTemp("", "atm-fake-gh-")
	if err == nil {
		err = copyAsGH(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakes = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func copyAsGH(dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	name := "gh"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	return os.WriteFile(filepath.Join(dir, name), b, 0o755)
}

func after(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// fakeGH logs its arguments and answers like GitHub: pr view from the FAKE_GH_* fixtures, and a pr merge
// bound to a head other than FAKE_GH_HEAD, the live one, is refused.
func fakeGH(args []string) int {
	f, err := os.OpenFile(os.Getenv("FAKE_GH_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	_, _ = fmt.Fprintln(f, strings.Join(args, " "))
	_ = f.Close()
	if os.Getenv("FAKE_GH_FAIL") != "" {
		return 1
	}
	commits := strings.Split(strings.TrimSuffix(os.Getenv("FAKE_GH_COMMITS"), "\n"), "\n")
	if len(args) > 0 && args[0] == "api" {
		if len(args) > 1 && !strings.HasSuffix(args[1], "/commits") {
			fmt.Println(os.Getenv("FAKE_GH_TOTAL"))
			return 0
		}
		if !slices.Contains(args, "--paginate") {
			commits = commits[:min(len(commits), 100)]
		}
		commits = commits[:min(len(commits), 250)]
		fmt.Println(strings.Join(commits, "\n"))
		return 0
	}
	switch after(args, "--json") {
	case "headRefOid,comments":
		fmt.Print(os.Getenv("FAKE_GH_PR"))
	case "body":
		fmt.Println(os.Getenv("FAKE_GH_BODY"))
	case "labels":
		fmt.Println(os.Getenv("FAKE_GH_LABELS"))
	}
	if head := after(args, "--match-head-commit"); head != "" && head != os.Getenv("FAKE_GH_HEAD") {
		fmt.Fprintln(os.Stderr, "head moved")
		return 1
	}
	return 0
}

type comment struct{ login, body string }

func cleared(head string) comment {
	return comment{"teseo", "No-mistakes reviewed: " + head + "\r\nabc1234: kept - meets criterion 1"}
}

// pr is a pull request as GitHub reports it: head is what pr view reads, live the head when pr merge runs.
type pr struct {
	head, live, body, labels string
	subjects                 []string
	comments                 []comment
	fail                     bool
}

// view is the gh --jq projection no-mistakes-review.sh asks for.
func (p pr) view() string {
	lines := []string{"head " + p.head}
	for _, c := range p.comments {
		for _, line := range strings.Split(c.body, "\n") {
			lines = append(lines, "comment "+c.login+" "+line)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

type result struct {
	out, calls string
	err        error
}

func (p pr) run(t *testing.T, command string) result {
	t.Helper()
	log := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("FAKE_GH_PR", p.view())
	commits := make([]string, 0, len(p.subjects))
	for _, subject := range p.subjects {
		commits = append(commits, "commit "+subject)
	}
	t.Setenv("FAKE_GH_COMMITS", strings.Join(commits, "\n"))
	t.Setenv("FAKE_GH_TOTAL", fmt.Sprint(len(p.subjects)))
	t.Setenv("FAKE_GH_HEAD", cmp.Or(p.live, p.head))
	t.Setenv("FAKE_GH_BODY", cmp.Or(p.body, lowRisk))
	t.Setenv("FAKE_GH_LABELS", p.labels)
	t.Setenv("FAKE_GH_FAIL", map[bool]string{true: "1"}[p.fail])
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = ".."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	calls, _ := os.ReadFile(log)
	return result{string(out) + stderr.String(), strings.ReplaceAll(string(calls), "\r\n", "\n"), err}
}

func nm(category string) []string {
	return []string{"feat: the unit", "ponytail: cut", "no-mistakes(" + category + "): added by the gate"}
}

func TestReviewPassesWithoutNoMistakesCommits(t *testing.T) {
	r := pr{head: newHead, subjects: []string{"feat: x", "fix: mention no-mistakes(review) in docs"}}.run(t, review)
	if r.err != nil || r.out != newHead+" none\n" {
		t.Fatalf("review = %q, %v; want %q", r.out, r.err, newHead+" none\n")
	}
}

func TestReviewBlocksEachNoMistakesCategory(t *testing.T) {
	for _, category := range []string{"review", "ci", "lint", "document", "test"} {
		t.Run(category, func(t *testing.T) {
			r := pr{head: newHead, subjects: nm(category)}.run(t, review)
			if r.err == nil || !strings.Contains(r.out, "No-mistakes reviewed: "+newHead) {
				t.Fatalf("review = %q, %v; want blocked until the head is reviewed", r.out, r.err)
			}
		})
	}
}

func TestReviewBlocksNoMistakesCommitAfterFirstHundred(t *testing.T) {
	subjects := make([]string, 101)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	subjects[100] = "no-mistakes(ci): added by the gate"
	r := pr{head: newHead, subjects: subjects}.run(t, review)
	if r.err == nil || !strings.Contains(r.out, "No-mistakes reviewed: "+newHead) ||
		!strings.Contains(r.calls, "--paginate") {
		t.Fatalf("review = %q, %v; calls = %q; want blocked after all commits", r.out, r.err, r.calls)
	}
}

func TestReviewFailsClosedBeyondGitHubCommitLimit(t *testing.T) {
	subjects := make([]string, 251)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	subjects[250] = "no-mistakes(ci): added by the gate"
	r := pr{head: newHead, subjects: subjects}.run(t, review)
	if r.err == nil || !strings.Contains(r.out, "commit list is incomplete: saw 250 of 251 commits") {
		t.Fatalf("review = %q, %v; want fail-closed incomplete list", r.out, r.err)
	}
}

func TestReviewClearsOnlyTheCoSCommentForTheCurrentHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    comment
		want string
	}{
		{"current", cleared(newHead), newHead + " cleared\n"},
		{"stale", cleared(oldHead), ""},
		{"other login", comment{"javi", "No-mistakes reviewed: " + newHead}, ""},
		{"quoted", comment{"teseo", "> No-mistakes reviewed: " + newHead}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pr{head: newHead, subjects: nm("review"), comments: []comment{tc.c}}.run(t, review)
			if tc.want == "" && r.err == nil || tc.want != "" && (r.err != nil || r.out != tc.want) {
				t.Fatalf("review = %q, %v; want %q", r.out, r.err, tc.want)
			}
		})
	}
}

func TestReviewFailsForAnotherHeadOrNoAnswer(t *testing.T) {
	moved := pr{head: newHead, subjects: nm("ci"), comments: []comment{cleared(oldHead), cleared(newHead)}}
	if r := moved.run(t, review+" "+oldHead); r.err == nil {
		t.Fatalf("a check for %s passed on head %s: %q", oldHead, newHead, r.out)
	}
	if r := (pr{head: newHead, fail: true}).run(t, review); r.err == nil {
		t.Fatalf("review passed without reading the PR: %q", r.out)
	}
}

func TestArmWithoutNoMistakesCommitsIsUnchanged(t *testing.T) {
	low := pr{head: newHead, subjects: []string{"feat: x"}}.run(t, arm)
	want := "pr merge 7 --auto --rebase --match-head-commit " + newHead + " --repo owner/repo"
	if low.err != nil || !strings.Contains(low.calls, want) || strings.Contains(low.calls, "pr edit") {
		t.Fatalf("low risk calls = %q, %v; want %q and no label", low.calls, low.err, want)
	}
	waiting := pr{head: newHead, subjects: []string{"feat: x"}, body: high}.run(t, arm)
	if waiting.err != nil || strings.Contains(waiting.calls, "pr merge") || !strings.Contains(waiting.out, "risk high") {
		t.Fatalf("high risk = %q, %q, %v; want waiting for risk-reviewed", waiting.out, waiting.calls, waiting.err)
	}
}

func TestArmLabelsAndWaitsWhileNoMistakesCommitsAreUnreviewed(t *testing.T) {
	r := pr{head: newHead, subjects: nm("test"), comments: []comment{cleared(oldHead)}}.run(t, arm)
	if r.err != nil || !strings.Contains(r.calls, "pr edit 7 --repo owner/repo --add-label review-required") ||
		strings.Contains(r.calls, "pr merge") {
		t.Fatalf("calls = %q, %v; want review-required and no merge", r.calls, r.err)
	}
}

func TestArmClearsTheLabelAndArmsTheReviewedHead(t *testing.T) {
	r := pr{head: newHead, subjects: nm("lint"), comments: []comment{cleared(newHead)}}.run(t, arm)
	if r.err != nil || !strings.Contains(r.calls, "pr edit 7 --repo owner/repo --remove-label review-required") ||
		!strings.Contains(r.calls, "pr merge 7 --auto --rebase --match-head-commit "+newHead) {
		t.Fatalf("calls = %q, %v; want the label removed and %s armed", r.calls, r.err, newHead)
	}
}

func TestArmKeepsHighRiskClearance(t *testing.T) {
	reviewed := pr{head: newHead, subjects: nm("document"), comments: []comment{cleared(newHead)}, body: high}
	if r := reviewed.run(t, arm); r.err != nil || strings.Contains(r.calls, "pr merge") {
		t.Fatalf("high risk without risk-reviewed: calls = %q, %v; want no merge", r.calls, r.err)
	}
	reviewed.labels = "review-required\nrisk-reviewed"
	if r := reviewed.run(t, arm); r.err != nil || !strings.Contains(r.calls, "pr merge 7 --auto") {
		t.Fatalf("high risk with risk-reviewed: calls = %q, %v; want armed", r.calls, r.err)
	}
}

func TestArmNeverArmsANewHeadWithOldClearance(t *testing.T) {
	r := pr{head: oldHead, live: newHead, subjects: nm("review"), comments: []comment{cleared(oldHead)}}.run(t, arm)
	if r.err == nil || !strings.Contains(r.calls, "--match-head-commit "+oldHead) {
		t.Fatalf("calls = %q, %v; want the merge bound to %s and refused on %s", r.calls, r.err, oldHead, newHead)
	}
}

type step struct {
	If, Run string
	Env     map[string]string
}

type workflow struct {
	On struct {
		PullRequest struct {
			Types []string `yaml:"types"`
		} `yaml:"pull_request"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Name  string `yaml:"name"`
		Steps []step `yaml:"steps"`
	} `yaml:"jobs"`
}

func load(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

var expression = regexp.MustCompile(`\$\{\{\s*(.+?)\s*\}\}`)

// expand fills a step's expressions the way Actions does for a pull_request event on PR 7 at head.
func expand(t *testing.T, run, head string) string {
	t.Helper()
	event := map[string]string{"github.event.pull_request.number": "7", "github.event.pull_request.head.sha": head}
	return expression.ReplaceAllStringFunc(run, func(m string) string {
		v, ok := event[expression.FindStringSubmatch(m)[1]]
		if !ok {
			t.Fatalf("unexpected expression %s", m)
		}
		return v
	})
}

// prStep is the Go checks step that runs only for pull requests.
func prStep(t *testing.T) step {
	t.Helper()
	w := load(t, "go.yml")
	if len(w.On.PullRequest.Types) != 0 {
		t.Fatalf("go.yml pull_request types = %v; want the default, which includes synchronize", w.On.PullRequest.Types)
	}
	summary := w.Jobs["summary"]
	for _, s := range summary.Steps {
		if summary.Name == "Go checks" && s.If == "github.event_name == 'pull_request'" {
			if s.Env["GH_TOKEN"] != "${{ github.token }}" {
				t.Fatalf("GH_TOKEN = %q", s.Env["GH_TOKEN"])
			}
			return s
		}
	}
	t.Fatal("the Go checks job has no pull request step")
	return step{}
}

func TestGoChecksFailUntilTheEventHeadIsReviewed(t *testing.T) {
	s := prStep(t)
	reviewedAt := func(head string) pr {
		return pr{head: newHead, subjects: nm("ci"), comments: []comment{cleared(head)}}
	}
	for _, tc := range []struct {
		name, event string
		p           pr
		pass        bool
	}{
		{"no no-mistakes commits", newHead, pr{head: newHead, subjects: []string{"feat: x"}}, true},
		{"armed PR gets an NM commit", newHead, reviewedAt(oldHead), false},
		{"reviewed head", newHead, reviewedAt(newHead), true},
		{"head moved after the event", oldHead, reviewedAt(oldHead), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := tc.p.run(t, expand(t, s.Run, tc.event)); (r.err == nil) != tc.pass {
				t.Fatalf("Go checks step = %q, %v; want pass %v", r.out, r.err, tc.pass)
			}
		})
	}
}

func TestAutoMergeLabelsAPRThatGetsANoMistakesCommit(t *testing.T) {
	w := load(t, "auto-merge.yml")
	if !slices.Contains(w.On.PullRequest.Types, "synchronize") {
		t.Fatalf("auto-merge.yml types = %v; want synchronize, a pushed commit", w.On.PullRequest.Types)
	}
	steps := w.Jobs["arm"].Steps
	r := pr{head: newHead, subjects: nm("review")}.run(t, expand(t, steps[len(steps)-1].Run, newHead))
	if r.err != nil || !strings.Contains(r.calls, "--add-label review-required") || strings.Contains(r.calls, "pr merge") {
		t.Fatalf("auto-merge calls = %q, %v; want review-required and no merge", r.calls, r.err)
	}
}

func TestGeneratedFilesAreMarked(t *testing.T) {
	out, err := exec.Command("git", "-C", "..", "check-attr", "linguist-generated", "--", "go.sum").Output()
	if err != nil || strings.TrimSpace(string(out)) != "go.sum: linguist-generated: set" {
		t.Fatalf("check-attr = %q, %v; want go.sum marked linguist-generated", out, err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		".gitattributes": "go.sum linguist-generated\n",
		"go.sum":         "generated\n",
		"main.go":        "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	out, err = exec.Command("git", "-C", dir, "diff", "--cached", "--stat", "--", ".",
		":(exclude,attr:linguist-generated)").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "main.go") || strings.Contains(string(out), "go.sum") {
		t.Fatalf("generated-file exclusion = %q, %v; want source without go.sum", out, err)
	}
}
