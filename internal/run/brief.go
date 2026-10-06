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

var briefTmpl = template.Must(template.New("brief.md").Funcs(template.FuncMap{
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
}).Parse(briefText))

// brief is brief.md for issue i under the repository's config c.
func brief(i Issue, c config.Config) (string, error) {
	var b strings.Builder
	err := briefTmpl.Execute(&b, struct {
		Issue
		config.Config
	}{i, c})
	return b.String(), err
}
