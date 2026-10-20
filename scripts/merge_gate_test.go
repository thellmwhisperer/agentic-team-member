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
	arm     = "scripts/arm-auto-merge.sh 7"
	lowRisk = "## Risk Assessment\nLow\n"
	high    = "## Risk Assessment\nHigh\n"
	armed   = "pr merge 7 --auto --rebase --match-head-commit " + newHead + " --repo owner/repo"
	disarm  = "pr merge 7 --disable-auto --repo owner/repo"
)

// fakes holds this test binary copied as gh.
var fakes string

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "gh" {
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
	return os.WriteFile(filepath.Join(dir, "gh"), b, 0o755)
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
	switch fields := after(args, "--json"); {
	case fields == "headRefOid":
		fmt.Println(os.Getenv("FAKE_GH_HEAD_READ"))
	case strings.Contains(fields, "comments"):
		fmt.Print(os.Getenv("FAKE_GH_COMMENTS"))
	case fields == "body":
		fmt.Println(os.Getenv("FAKE_GH_BODY"))
	case fields == "headRefName,files":
		fmt.Print(os.Getenv("FAKE_GH_FILES"))
	case strings.Contains(fields, "labels"):
		fmt.Print(os.Getenv("FAKE_GH_LABELS"))
	}
	if head := after(args, "--match-head-commit"); head != "" && head != os.Getenv("FAKE_GH_HEAD") {
		fmt.Fprintln(os.Stderr, "head moved")
		return 1
	}
	return 0
}

type comment struct{ login, body string }

// cleared is the unblocking comment Decision 7524 removed: it must never arm anything.
func cleared(head string) comment {
	return comment{"teseo", "No-mistakes reviewed: " + head + "\r\nabc1234: kept - meets criterion 1"}
}

// pr is a pull request as GitHub reports it: head is what pr view reads, live the head when pr merge runs.
type pr struct {
	head, live, body, branch string
	labels, subjects, files  []string
	comments                 []comment
	autoMerge, fail          bool
}

// view is the gh --jq projection of the PR's auto-merge state and labels that arm-auto-merge.sh asks for.
func (p pr) view() string {
	lines := []string{fmt.Sprint("armed ", p.autoMerge)}
	for _, l := range p.labels {
		lines = append(lines, "label "+l)
	}
	return strings.Join(lines, "\n") + "\n"
}

// changes is the gh --jq projection of the PR's branch and changed files that arm-auto-merge.sh asks for.
func (p pr) changes() string {
	lines := []string{"branch " + cmp.Or(p.branch, "feat/unit")}
	for _, f := range p.files {
		lines = append(lines, "file "+f)
	}
	return strings.Join(lines, "\n") + "\n"
}

// talk is the PR's comments, served to any query for them so a test shows that none of them arms.
func (p pr) talk() string {
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
	t.Setenv("FAKE_GH_HEAD_READ", p.head)
	t.Setenv("FAKE_GH_COMMENTS", p.talk())
	commits := make([]string, 0, len(p.subjects))
	for _, subject := range p.subjects {
		commits = append(commits, "commit "+subject)
	}
	t.Setenv("FAKE_GH_COMMITS", strings.Join(commits, "\n"))
	t.Setenv("FAKE_GH_TOTAL", fmt.Sprint(len(p.subjects)))
	t.Setenv("FAKE_GH_HEAD", cmp.Or(p.live, p.head))
	t.Setenv("FAKE_GH_BODY", cmp.Or(p.body, lowRisk))
	t.Setenv("FAKE_GH_LABELS", p.view())
	t.Setenv("FAKE_GH_FILES", p.changes())
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

// held reports whether a run left the PR unarmed with review-required added and no error.
func held(r result) bool {
	return r.err == nil && strings.Contains(r.calls, "pr edit 7 --repo owner/repo --add-label review-required") &&
		!strings.Contains(r.calls, "--auto")
}

func TestArmIgnoresNoMistakesMentionsOutsideTheSubjectPrefix(t *testing.T) {
	r := pr{head: newHead, subjects: []string{"feat: x", "fix: mention no-mistakes(review) in docs"}}.run(t, arm+" opened")
	if r.err != nil || !strings.Contains(r.calls, armed) || strings.Contains(r.calls, "pr edit") {
		t.Fatalf("calls = %q, %v; want %q and no label", r.calls, r.err, armed)
	}
}

func TestArmLabelsEachNoMistakesCategory(t *testing.T) {
	for _, category := range []string{"review", "ci", "lint", "document", "test"} {
		t.Run(category, func(t *testing.T) {
			r := pr{head: newHead, subjects: nm(category)}.run(t, arm+" opened")
			if !held(r) {
				t.Fatalf("calls = %q, %v; want review-required and no arm", r.calls, r.err)
			}
		})
	}
}

func TestArmLabelsNoMistakesCommitAfterFirstHundred(t *testing.T) {
	subjects := make([]string, 101)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	subjects[100] = "no-mistakes(ci): added by the gate"
	r := pr{head: newHead, subjects: subjects}.run(t, arm+" opened")
	if !held(r) || !strings.Contains(r.calls, "--paginate") {
		t.Fatalf("calls = %q, %v; want every commit read, review-required and no arm", r.calls, r.err)
	}
}

func TestArmFailsClosedBeyondGitHubCommitLimit(t *testing.T) {
	subjects := make([]string, 251)
	for i := range subjects {
		subjects[i] = "feat: ordinary commit"
	}
	subjects[250] = "no-mistakes(ci): added by the gate"
	r := pr{head: newHead, subjects: subjects, labels: []string{"review-completed"}}.run(t, arm+" labeled")
	if !held(r) || !strings.Contains(r.out, "commit list is incomplete: saw 250 of 251 commits") {
		t.Fatalf("out = %q, calls = %q, %v; want fail-closed incomplete list", r.out, r.calls, r.err)
	}
}

func TestArmWaitsForReviewCompletedWhateverTheComments(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    []comment
	}{
		{"none", nil},
		{"current head", []comment{cleared(newHead)}},
		{"stale head", []comment{cleared(oldHead)}},
		{"other login", []comment{{"javi", "No-mistakes reviewed: " + newHead}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pr{head: newHead, subjects: nm("review"), comments: tc.c, labels: []string{"review-required"}}
			if r := p.run(t, arm+" labeled"); !held(r) {
				t.Fatalf("calls = %q, %v; want no arm without review-completed", r.calls, r.err)
			}
		})
	}
}

func TestArmArmsWhenReviewCompletedIsAdded(t *testing.T) {
	p := pr{head: newHead, subjects: nm("ci"), labels: []string{"review-required", "review-completed"}}
	r := p.run(t, arm+" labeled")
	if r.err != nil || !strings.Contains(r.calls, armed) || strings.Contains(r.calls, "--remove-label") {
		t.Fatalf("calls = %q, out = %q, %v; want %q", r.calls, r.out, r.err, armed)
	}
}

func TestArmFailsWhenGitHubDoesNotAnswer(t *testing.T) {
	r := pr{head: newHead, subjects: nm("ci"), labels: []string{"review-completed"}, fail: true}.run(t, arm+" labeled")
	if r.err == nil || strings.Contains(r.calls, "--auto") {
		t.Fatalf("calls = %q, %v; want a failure and no arm", r.calls, r.err)
	}
}

func TestArmWithoutNoMistakesCommitsIsUnchanged(t *testing.T) {
	for _, event := range []string{"opened", "labeled", "synchronize"} {
		low := pr{head: newHead, subjects: []string{"feat: x"}}.run(t, arm+" "+event)
		if low.err != nil || !strings.Contains(low.calls, armed) || strings.Contains(low.calls, "pr edit") {
			t.Fatalf("%s: low risk calls = %q, %v; want %q and no label", event, low.calls, low.err, armed)
		}
	}
	waiting := pr{head: newHead, subjects: []string{"feat: x"}, body: high}.run(t, arm+" opened")
	if waiting.err != nil || strings.Contains(waiting.calls, "pr merge") || !strings.Contains(waiting.out, "risk high") {
		t.Fatalf("high risk = %q, %q, %v; want waiting for risk-reviewed", waiting.out, waiting.calls, waiting.err)
	}
}

func TestArmKeepsHighRiskClearance(t *testing.T) {
	reviewed := pr{head: newHead, subjects: nm("document"), body: high,
		labels: []string{"review-required", "review-completed"}}
	if r := reviewed.run(t, arm+" labeled"); r.err != nil || strings.Contains(r.calls, "pr merge") {
		t.Fatalf("high risk without risk-reviewed: calls = %q, %v; want no merge", r.calls, r.err)
	}
	reviewed.labels = append(reviewed.labels, "risk-reviewed")
	if r := reviewed.run(t, arm+" labeled"); r.err != nil || !strings.Contains(r.calls, armed) {
		t.Fatalf("high risk with risk-reviewed: calls = %q, %v; want armed", r.calls, r.err)
	}
}

func TestArmArmsAReleasePleasePR(t *testing.T) {
	release := pr{head: newHead, subjects: []string{"chore(main): release 1.1.3"},
		body: ":robot: I have created a release", branch: "release-please--branches--main",
		files: []string{"CHANGELOG.md", ".release-please-manifest.json"}}
	r := release.run(t, arm+" opened")
	if r.err != nil || !strings.Contains(r.calls, armed) || strings.Contains(r.calls, "pr edit") {
		t.Fatalf("release PR: calls = %q, out = %q, %v; want %q and no label", r.calls, r.out, r.err, armed)
	}
	release.files = append(release.files, "go.mod")
	r = release.run(t, arm+" opened")
	if r.err != nil || strings.Contains(r.calls, "pr merge") || !strings.Contains(r.out, "risk high") {
		t.Fatalf("release branch changing go.mod: out = %q, calls = %q, %v; want risk-reviewed", r.out, r.calls, r.err)
	}
	release.autoMerge = true
	for _, event := range []string{"opened", "reopened", "ready_for_review", "labeled", "synchronize"} {
		r = release.run(t, arm+" "+event)
		if r.err != nil || !strings.Contains(r.calls, disarm) || strings.Contains(r.calls, "--auto ") ||
			strings.Contains(r.calls, "pr edit") || !strings.Contains(r.out, "risk high") {
			t.Fatalf("%s: release lost exemption: calls = %q, out = %q, %v; "+
				"want disabled auto-merge without label changes", event, r.calls, r.out, r.err)
		}
	}
}

func TestArmArmsAReleasePleasePRWithRiskClearance(t *testing.T) {
	release := pr{head: newHead, subjects: []string{"chore(main): release 1.1.3"},
		body: ":robot: I have created a release", branch: "release-please--branches--main",
		files:     []string{"CHANGELOG.md", ".release-please-manifest.json", "go.mod"},
		autoMerge: true, labels: []string{"risk-reviewed"}}
	r := release.run(t, arm+" synchronize")
	if r.err != nil || !strings.Contains(r.calls, armed) || strings.Contains(r.calls, disarm) {
		t.Fatalf("release with risk clearance: calls = %q, %v; want armed", r.calls, r.err)
	}
	release.labels = nil
	for _, level := range []string{"Low", "Medium"} {
		release.body = "## Risk Assessment\n" + level + "\n"
		r = release.run(t, arm+" synchronize")
		if r.err != nil || !strings.Contains(r.calls, armed) || strings.Contains(r.calls, disarm) {
			t.Fatalf("%s risk release changing go.mod: calls = %q, %v; want armed", level, r.calls, r.err)
		}
	}
	release.branch = "feat/unit"
	release.body = high
	r = release.run(t, arm+" synchronize")
	if r.err != nil || strings.Contains(r.calls, "pr merge") || !strings.Contains(r.out, "risk high") {
		t.Fatalf("ordinary high risk PR: calls = %q, out = %q, %v; want existing waiting behavior", r.calls, r.out, r.err)
	}
}

func TestArmNeverArmsADiscardedPR(t *testing.T) {
	for _, tc := range []struct {
		name     string
		subjects []string
	}{
		{"no-mistakes commits", nm("review")},
		{"plain commits", []string{"feat: x"}},
	} {
		for _, autoMerge := range []bool{false, true} {
			t.Run(fmt.Sprint(tc.name, " armed ", autoMerge), func(t *testing.T) {
				p := pr{head: newHead, subjects: tc.subjects, autoMerge: autoMerge,
					labels: []string{"review-required", "review-completed", "risk-reviewed", "pr-discarded"}}
				r := p.run(t, arm+" labeled")
				if r.err != nil || strings.Contains(r.calls, "--auto ") || strings.Contains(r.calls, disarm) != autoMerge {
					t.Fatalf("calls = %q, %v; want no arm, and disarmed only if armed", r.calls, r.err)
				}
			})
		}
	}
}

func TestAnyPushRemovesReviewCompleted(t *testing.T) {
	for _, autoMerge := range []bool{false, true} {
		t.Run(fmt.Sprint("armed ", autoMerge), func(t *testing.T) {
			p := pr{head: newHead, subjects: nm("lint"), autoMerge: autoMerge,
				labels: []string{"review-required", "review-completed"}}
			r := p.run(t, arm+" synchronize")
			if !held(r) || !strings.Contains(r.calls, "pr edit 7 --repo owner/repo --remove-label review-completed") ||
				strings.Contains(r.calls, disarm) != autoMerge {
				t.Fatalf("calls = %q, %v; want review-completed removed, no arm, disarmed only if armed", r.calls, r.err)
			}
		})
	}
}

var labelDeleted = regexp.MustCompile(`^api -X DELETE \S+/labels/(\S+)$`)

// ends replays the label edits in calls on labels, returning the labels the PR ends the run with.
func ends(labels []string, calls string) []string {
	set := map[string]bool{}
	for _, l := range labels {
		set[l] = true
	}
	for _, call := range strings.Split(calls, "\n") {
		args := strings.Fields(call)
		if l := after(args, "--add-label"); l != "" {
			set[l] = true
		}
		if l := after(args, "--remove-label"); l != "" {
			delete(set, l)
		}
		if m := labelDeleted.FindStringSubmatch(call); m != nil {
			delete(set, m[1])
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}

func TestPREndsWithOnlyOneReviewLabel(t *testing.T) {
	both := []string{"review-required", "review-completed"}
	for _, tc := range []struct {
		event string
		armed bool
		want  string
	}{
		{"labeled", true, "review-completed"},
		{"synchronize", false, "review-required"},
	} {
		t.Run(tc.event, func(t *testing.T) {
			r := pr{head: newHead, subjects: nm("review"), labels: both}.run(t, arm+" "+tc.event)
			got := ends(both, r.calls)
			if r.err != nil || strings.Contains(r.calls, armed) != tc.armed || !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("calls = %q, %v; labels = %q, want only %q, armed %v", r.calls, r.err, got, tc.want, tc.armed)
			}
		})
	}
}

func TestArmNeverArmsAHeadDetectionDidNotRead(t *testing.T) {
	p := pr{head: oldHead, live: newHead, subjects: nm("review"), labels: []string{"review-completed"}}
	if r := p.run(t, arm+" labeled"); r.err == nil || !strings.Contains(r.calls, "--match-head-commit "+oldHead) {
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

// expand fills a step's expressions the way Actions does for a pull_request event of this action on PR 7.
func expand(t *testing.T, run, action string) string {
	t.Helper()
	event := map[string]string{"github.event.pull_request.number": "7", "github.event.action": action}
	return expression.ReplaceAllStringFunc(run, func(m string) string {
		v, ok := event[expression.FindStringSubmatch(m)[1]]
		if !ok {
			t.Fatalf("unexpected expression %s", m)
		}
		return v
	})
}

func TestGoChecksRunNoNoMistakesReviewStep(t *testing.T) {
	w := load(t, "go.yml")
	summary := w.Jobs["summary"]
	if summary.Name != "Go checks" {
		t.Fatalf("summary job name = %q; want Go checks", summary.Name)
	}
	var runs []string
	for _, s := range summary.Steps {
		if s.Run != "" {
			runs = append(runs, s.Run)
		}
	}
	want := `scripts/ci-summary.sh "${{ needs.changes.result }}" "${{ needs.test.result }}"`
	if len(runs) != 1 || runs[0] != want {
		t.Fatalf("Go checks runs %q; want only %q, tests and lint passed or skipped", runs, want)
	}
}

func TestAutoMergeRunsTheGateForEachEvent(t *testing.T) {
	w := load(t, "auto-merge.yml")
	types := w.On.PullRequest.Types
	if !slices.Contains(types, "synchronize") || !slices.Contains(types, "labeled") ||
		slices.Contains(types, "unlabeled") {
		t.Fatalf("auto-merge.yml types = %v; want synchronize and labeled, and removing a label not a trigger", types)
	}
	steps := w.Jobs["arm"].Steps
	run := steps[len(steps)-1].Run
	if r := (pr{head: newHead, subjects: nm("review")}).run(t, expand(t, run, "synchronize")); !held(r) {
		t.Fatalf("synchronize calls = %q, %v; want review-required and no merge", r.calls, r.err)
	}
	completed := pr{head: newHead, subjects: nm("review"), labels: []string{"review-required", "review-completed"}}
	if r := completed.run(t, expand(t, run, "labeled")); r.err != nil || !strings.Contains(r.calls, armed) {
		t.Fatalf("labeled calls = %q, %v; want %q", r.calls, r.err, armed)
	}
	r := completed.run(t, expand(t, run, "synchronize"))
	if !held(r) || !strings.Contains(r.calls, "--remove-label review-completed") {
		t.Fatalf("synchronize calls = %q, %v; want review-completed removed and no merge", r.calls, r.err)
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
