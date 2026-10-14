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
	merge   = "pr merge 7 --auto --rebase"
	disarm  = "pr merge 7 --disable-auto --repo owner/repo"
	lowRisk = "## Risk Assessment\nLow\n"
	high    = "## Risk Assessment\nHigh\n"
	labels  = "review-required\nreview-completed"
)

// sha is the fake sha of the PR's i-th commit, counting from 0.
func sha(i int) string { return fmt.Sprintf("%040x", i) }

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
	case "headRefOid":
		fmt.Println(os.Getenv("FAKE_GH_PR"))
	case "comments":
		fmt.Print(os.Getenv("FAKE_GH_COMMENTS"))
	case "body":
		fmt.Println(os.Getenv("FAKE_GH_BODY"))
	case "labels":
		fmt.Println(os.Getenv("FAKE_GH_LABELS"))
	case "autoMergeRequest":
		fmt.Println(os.Getenv("FAKE_GH_ARMED"))
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

// pr is a pull request as GitHub reports it: head is what pr view reads, live the head when pr merge runs,
// armed whether auto-merge is on.
type pr struct {
	head, live, body, labels string
	subjects                 []string
	comments                 []comment
	fail, armed              bool
}

// view lists the PR's comments, which no label decision may read.
func (p pr) view() string {
	var lines []string
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
	t.Setenv("FAKE_GH_PR", p.head)
	t.Setenv("FAKE_GH_COMMENTS", p.view())
	t.Setenv("FAKE_GH_ARMED", fmt.Sprint(p.armed))
	commits := make([]string, 0, len(p.subjects))
	for i, subject := range p.subjects {
		commits = append(commits, "commit "+subject+" "+sha(i))
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

func TestReviewDetectsEachNoMistakesCategory(t *testing.T) {
	for _, category := range []string{"review", "ci", "lint", "document", "test"} {
		t.Run(category, func(t *testing.T) {
			r := pr{head: newHead, subjects: nm(category)}.run(t, review)
			if r.err != nil || r.out != newHead+" nm\n" {
				t.Fatalf("review = %q, %v; want %q", r.out, r.err, newHead+" nm\n")
			}
		})
	}
}

func TestReviewDetectsNoMistakesCommitAfterFirstHundred(t *testing.T) {
	subjects := make([]string, 101)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	subjects[100] = "no-mistakes(ci): added by the gate"
	r := pr{head: newHead, subjects: subjects}.run(t, review)
	if r.err != nil || r.out != newHead+" nm\n" || !strings.Contains(r.calls, "--paginate") {
		t.Fatalf("review = %q, %v; calls = %q; want detected after all commits", r.out, r.err, r.calls)
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

func TestReviewMarksNoMistakesCommitsPushedAfterThePreviousHead(t *testing.T) {
	for _, tc := range []struct {
		name, before string
		subjects     []string
		want         string
	}{
		{"pushed after", sha(1), nm("review"), "new"},
		{"previous head gone, a force push", oldHead, nm("review"), "new"},
		{"already there", sha(2), nm("review"), "nm"},
		{"only an ordinary commit since", sha(2), append(nm("review"), "fix: by hand"), "nm"},
		{"not a push", "", nm("review"), "nm"},
		{"no no-mistakes commit", sha(0), []string{"feat: x", "fix: y"}, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pr{head: newHead, subjects: tc.subjects}.run(t, review+" '"+tc.before+"'")
			if r.err != nil || r.out != newHead+" "+tc.want+"\n" {
				t.Fatalf("review = %q, %v; want %q", r.out, r.err, newHead+" "+tc.want+"\n")
			}
		})
	}
}

func TestReviewFailsWithoutAnAnswer(t *testing.T) {
	if r := (pr{subjects: nm("ci")}).run(t, review); r.err == nil {
		t.Fatalf("review passed without a head: %q", r.out)
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
	r := pr{head: newHead, subjects: nm("test"), armed: true}.run(t, arm)
	if r.err != nil || !strings.Contains(r.calls, "pr edit 7 --repo owner/repo --add-label review-required") ||
		!strings.Contains(r.calls, disarm) || strings.Contains(r.calls, merge) {
		t.Fatalf("calls = %q, %v; want review-required, auto-merge disabled and no arm", r.calls, r.err)
	}
}

func TestArmIgnoresCommentsWithoutReviewCompleted(t *testing.T) {
	for _, c := range []comment{cleared(newHead), {"javi", "review-completed"}} {
		r := pr{head: newHead, subjects: nm("lint"), labels: "review-required", comments: []comment{c}}.run(t, arm)
		if r.err != nil || strings.Contains(r.calls, merge) || strings.Contains(r.calls, "pr edit") {
			t.Fatalf("comment %q: calls = %q, %v; want no arm and no label change", c.body, r.calls, r.err)
		}
	}
}

func TestArmArmsTheHeadOnceReviewCompleted(t *testing.T) {
	r := pr{head: newHead, subjects: nm("lint"), labels: labels}.run(t, arm)
	if r.err != nil || !strings.Contains(r.calls, merge+" --match-head-commit "+newHead) ||
		strings.Contains(r.calls, "pr edit") {
		t.Fatalf("calls = %q, %v; want %s armed and the labels left alone", r.calls, r.err, newHead)
	}
}

func TestArmKeepsHighRiskClearance(t *testing.T) {
	reviewed := pr{head: newHead, subjects: nm("document"), labels: labels, body: high}
	if r := reviewed.run(t, arm); r.err != nil || strings.Contains(r.calls, "pr merge") {
		t.Fatalf("high risk without risk-reviewed: calls = %q, %v; want no merge", r.calls, r.err)
	}
	reviewed.labels = "review-required\nrisk-reviewed"
	if r := reviewed.run(t, arm); r.err != nil || strings.Contains(r.calls, merge) {
		t.Fatalf("risk-reviewed without review-completed: calls = %q, %v; want no merge", r.calls, r.err)
	}
	reviewed.labels = labels + "\nrisk-reviewed"
	if r := reviewed.run(t, arm); r.err != nil || !strings.Contains(r.calls, merge) {
		t.Fatalf("high risk with risk-reviewed: calls = %q, %v; want armed", r.calls, r.err)
	}
}

func TestArmNeverArmsANewHeadItDidNotJudge(t *testing.T) {
	r := pr{head: oldHead, live: newHead, subjects: nm("review"), labels: labels}.run(t, arm)
	if r.err == nil || !strings.Contains(r.calls, "--match-head-commit "+oldHead) {
		t.Fatalf("calls = %q, %v; want the merge bound to %s and refused on %s", r.calls, r.err, oldHead, newHead)
	}
}

func TestArmNeverArmsADiscardedPR(t *testing.T) {
	for _, p := range []pr{
		{head: newHead, subjects: nm("ci"), labels: labels + "\npr-discarded"},
		{head: newHead, subjects: []string{"feat: x"}, labels: "pr-discarded"},
	} {
		if r := p.run(t, arm); r.err != nil || strings.Contains(r.calls, "pr merge") {
			t.Fatalf("unarmed %q: calls = %q, %v; want no merge call", p.labels, r.calls, r.err)
		}
		p.armed = true
		if r := p.run(t, arm); r.err != nil || !strings.Contains(r.calls, disarm) || strings.Contains(r.calls, merge) {
			t.Fatalf("armed %q: calls = %q, %v; want auto-merge disabled", p.labels, r.calls, r.err)
		}
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

// expand fills a step's expressions the way Actions does for a pull_request event on PR 7; before is the
// previous head on synchronize and empty on every other action.
func expand(t *testing.T, run, before string) string {
	t.Helper()
	event := map[string]string{"github.event.pull_request.number": "7", "github.event.before": before}
	return expression.ReplaceAllStringFunc(run, func(m string) string {
		v, ok := event[expression.FindStringSubmatch(m)[1]]
		if !ok {
			t.Fatalf("unexpected expression %s", m)
		}
		return v
	})
}

// armStep is the auto-merge.yml step as it runs on the pull_request action.
func armStep(t *testing.T, action, before string) string {
	t.Helper()
	w := load(t, "auto-merge.yml")
	if !slices.Contains(w.On.PullRequest.Types, action) {
		t.Fatalf("auto-merge.yml types = %v; want %s", w.On.PullRequest.Types, action)
	}
	steps := w.Jobs["arm"].Steps
	return expand(t, steps[len(steps)-1].Run, before)
}

func TestGoChecksRunNoNoMistakesReview(t *testing.T) {
	w := load(t, "go.yml")
	if w.Jobs["summary"].Name != "Go checks" {
		t.Fatalf("summary job name = %q; want Go checks", w.Jobs["summary"].Name)
	}
	for name, job := range w.Jobs {
		for _, s := range job.Steps {
			if strings.Contains(s.Run, "no-mistakes") || s.Env["GH_TOKEN"] != "" {
				t.Fatalf("go.yml job %s runs %q with %v; want no no-mistakes review", name, s.Run, s.Env)
			}
		}
	}
}

func TestAutoMergeArmsWhenReviewCompletedIsAdded(t *testing.T) {
	r := pr{head: newHead, subjects: nm("ci"), labels: labels}.run(t, armStep(t, "labeled", ""))
	if r.err != nil || !strings.Contains(r.calls, merge+" --match-head-commit "+newHead) {
		t.Fatalf("auto-merge calls = %q, %v; want %s armed", r.calls, r.err, newHead)
	}
}

func TestAutoMergeLabelsAPRThatGetsANoMistakesCommit(t *testing.T) {
	r := pr{head: newHead, subjects: nm("review"), armed: true}.run(t, armStep(t, "synchronize", sha(1)))
	if r.err != nil || !strings.Contains(r.calls, "--add-label review-required") ||
		!strings.Contains(r.calls, disarm) || strings.Contains(r.calls, merge) {
		t.Fatalf("auto-merge calls = %q, %v; want review-required, auto-merge disabled and no arm", r.calls, r.err)
	}
}

func TestAutoMergeTakesReviewCompletedOffANewNoMistakesCommit(t *testing.T) {
	p := pr{head: newHead, subjects: append(nm("ci"), "no-mistakes(lint): again"), labels: labels, armed: true}
	r := p.run(t, armStep(t, "synchronize", sha(2)))
	if r.err != nil || !strings.Contains(r.calls, "pr edit 7 --repo owner/repo --remove-label review-completed") ||
		!strings.Contains(r.calls, disarm) || strings.Contains(r.calls, merge) {
		t.Fatalf("auto-merge calls = %q, %v; want review-completed removed and no arm", r.calls, r.err)
	}
	if r := p.run(t, armStep(t, "synchronize", sha(3))); r.err != nil || !strings.Contains(r.calls, merge) {
		t.Fatalf("push without a new no-mistakes commit: calls = %q, %v; want armed", r.calls, r.err)
	}
}

func TestAutoMergeFailsClosedBeyondGitHubCommitLimit(t *testing.T) {
	subjects := make([]string, 251)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	r := pr{head: newHead, subjects: subjects, labels: "review-completed"}.run(t, armStep(t, "labeled", ""))
	if r.err != nil || !strings.Contains(r.calls, "--add-label review-required") || strings.Contains(r.calls, merge) {
		t.Fatalf("auto-merge calls = %q, %v; want review-required and no arm", r.calls, r.err)
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
