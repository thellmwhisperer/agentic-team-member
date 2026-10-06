package run

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

// contract is the fields of brief.md.
type contract struct {
	Title, Body, Clone, BaseRef, Test, Typecheck string
	Forbidden, Scope                             []string
}

// brief is brief.md, the contract the agent works to: today's build_brief, sections and wording.
//
//go:embed brief.md.tmpl
var briefText string

var brief = template.Must(template.New("brief.md").Funcs(template.FuncMap{"code": func(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}}).Parse(briefText))

// parseIssue is the title, the first line without its leading #s, and the rest of the issue text.
func parseIssue(text string) (title, body string) {
	first, rest, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(strings.TrimLeft(first, "# ")), strings.TrimSpace(rest)
}

var (
	pathToken     = regexp.MustCompile(`[\w./-]+`)
	backtickIdent = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
)

func isAtmPath(path string) bool { return path == ".atm" || strings.HasPrefix(path, ".atm/") }

// deriveScope is the tracked paths the issue text names, plus the files defining the identifiers it
// backticks in Go, TS/JS or Python.
func deriveScope(text, clone string) []string {
	tracked := map[string]bool{}
	for _, path := range gitLines(clone, "ls-files") {
		tracked[path] = true
	}
	var paths []string
	for _, token := range pathToken.FindAllString(text, -1) {
		if path := strings.TrimPrefix(strings.TrimRight(token, ".,:;"), "./"); tracked[path] {
			paths = append(paths, path)
		}
	}
	for _, m := range backtickIdent.FindAllStringSubmatch(text, -1) {
		n := regexp.QuoteMeta(m[1])
		args := []string{"grep", "-l", "-E"}
		for _, pattern := range []string{`^func (\([^)]*\) )?` + n + `\(`, `^(export )?(async )?function ` + n + `\(`,
			`^(export )?const ` + n + ` =`, `^(async )?def ` + n + `\(`} {
			args = append(args, "-e", pattern)
		}
		paths = append(paths, gitLines(clone, args...)...)
	}
	paths = slices.DeleteFunc(paths, isAtmPath)
	slices.Sort(paths)
	return slices.Compact(paths)
}

// scopeOf is the globs of --scope, or those the issue names; none means open.
func scopeOf(globs []string, title, body, clone string) (source string, scope []string) {
	if len(globs) > 0 {
		return "flag", globs
	}
	if scope = deriveScope(fmt.Sprintf("%s\n%s", title, body), clone); len(scope) > 0 {
		return "issue", scope
	}
	return "open", nil
}
