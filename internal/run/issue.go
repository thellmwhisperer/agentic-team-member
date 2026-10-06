package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Issue is what the run fixes. Number is set only when it came from GitHub.
type Issue struct {
	Number int
	Title  string
	Body   string
	Type   string // one of taskTypes' values
}

// taskTypes maps what an issue may declare in a "Type: <type>" line to its task type.
var taskTypes = map[string]string{
	"fix": "fix", "hotfix": "fix", "feature": "feature", "greenfield": "greenfield", "refactor": "refactor",
	"tests": "tests", "docs": "docs", "chore": "chore",
}

var typeLine = regexp.MustCompile(`(?im)^type:[ \t]*(\S+)[ \t]*$`)

// readIssue reads arg, a file or, when it is a number, an issue of the GitHub repository repo.
func readIssue(arg, repo string) (Issue, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		return fromGitHub(n, repo)
	}
	b, err := os.ReadFile(arg)
	if err != nil {
		return Issue{}, fmt.Errorf("issue: %w", err)
	}
	title, body, _ := strings.Cut(string(b), "\n")
	return parse(Issue{Title: strings.TrimLeft(strings.TrimSpace(title), "#"), Body: body})
}

func fromGitHub(n int, repo string) (Issue, error) {
	if repo == "" {
		return Issue{}, fmt.Errorf("issue %d: origin is not a GitHub repository", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(n), "--repo", repo, "--json", "title,body")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Issue{}, fmt.Errorf("gh issue view %d: %w: %s", n, err, strings.TrimSpace(stderr.String()))
	}
	var data struct{ Title, Body string }
	if err := json.Unmarshal(out, &data); err != nil {
		return Issue{}, fmt.Errorf("gh issue view %d returned no JSON: %w", n, err)
	}
	return parse(Issue{Number: n, Title: data.Title, Body: data.Body})
}

// parse trims the issue and sets its task type, or says why it is not one ATM can run.
func parse(i Issue) (Issue, error) {
	i.Title, i.Body = strings.TrimSpace(i.Title), strings.TrimSpace(i.Body)
	switch {
	case i.Title == "":
		return i, errors.New("issue: empty title")
	case i.Body == "":
		return i, errors.New("issue: empty body")
	}
	m := typeLine.FindStringSubmatch(i.Body)
	if m == nil {
		return i, errors.New(`issue: task type missing, want a "Type: <type>" line in the body`)
	}
	if i.Type = taskTypes[strings.ToLower(m[1])]; i.Type == "" {
		return i, fmt.Errorf("issue: task type %q unknown, want fix, hotfix, feature, greenfield, refactor, "+
			"tests, docs or chore", m[1])
	}
	return i, nil
}
