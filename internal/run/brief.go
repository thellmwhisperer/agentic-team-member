package run

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// briefText is brief.md, the contract the agent works to.
//
//go:embed brief.md.tmpl
var briefText string

// ponytailText is brief-ponytail.md, the contract of the slop detector.
//
//go:embed brief-ponytail.md.tmpl
var ponytailText string

var funcs = template.FuncMap{
	"code": func(items []string) string {
		if len(items) == 0 {
			return "none"
		}
		return "`" + strings.Join(items, "`, `") + "`"
	},
	"cmd": func(c string) string {
		if c == "" {
			return "none"
		}
		return "`" + c + "`"
	},
}

// brief is brief.md for issue i under the repository's config c, whose agent's scratch directory is scratch.
func brief(i Issue, c config.Config, scratch, text string) (string, error) {
	return renderBrief(text, struct {
		Issue
		config.Config
		Scratch string
	}{i, c, scratch})
}

// ponytailBrief is brief-ponytail.md for issue i, under the repository's config c, whose run made diff and proofs.
func ponytailBrief(i Issue, c config.Config, diff string, proofs []proof, text string) (string, error) {
	var tests []string
	for _, p := range proofs {
		if p.test != "" {
			tests = append(tests, p.test)
		}
	}
	return renderBrief(text, struct {
		Issue
		config.Config
		Diff     string
		Families []string
		Tests    []string
	}{i, c, diff, families, tests})
}

func renderBrief(text string, data any) (string, error) {
	t, err := template.New("brief").Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	err = t.Execute(&b, data)
	return b.String(), err
}
