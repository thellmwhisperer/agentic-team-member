package run

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// briefText is brief.md, the contract the agent works to; its ACCEPTANCE follows the task type.
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

var (
	briefTmpl    = template.Must(template.New("brief.md").Funcs(funcs).Parse(briefText))
	ponytailTmpl = template.Must(template.New("brief-ponytail.md").Funcs(funcs).Parse(ponytailText))
)

// brief is brief.md for issue i under the repository's config c.
func brief(i Issue, c config.Config) (string, error) {
	var b strings.Builder
	err := briefTmpl.Execute(&b, struct {
		Issue
		config.Config
	}{i, c})
	return b.String(), err
}

// ponytailBrief is brief-ponytail.md for issue i, under the repository's config c, whose run made diff.
func ponytailBrief(i Issue, c config.Config, diff string) (string, error) {
	var b strings.Builder
	err := ponytailTmpl.Execute(&b, struct {
		Issue
		config.Config
		Diff     string
		Families []string
	}{i, c, diff, families})
	return b.String(), err
}
