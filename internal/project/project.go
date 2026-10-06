// Package project is the repository ATM works on: its root, its GitHub repository and .atm/, where
// everything ATM writes goes.
package project

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Project is the git repository around a directory.
type Project struct {
	Root   string // git rev-parse --show-toplevel
	GitHub string // owner/repo of origin, empty when origin is missing or not on GitHub
}

var githubURL = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// Find is the project around dir.
func Find(dir string) (Project, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return Project{}, fmt.Errorf("%s is not in a git repository: %w", dir, err)
	}
	p := Project{Root: strings.TrimSpace(string(out))}
	// No origin is not an error: the project just has no GitHub repository.
	if url, err := exec.Command("git", "-C", p.Root, "remote", "get-url", "origin").Output(); err == nil {
		if m := githubURL.FindStringSubmatch(strings.TrimSpace(string(url))); m != nil {
			p.GitHub = m[1]
		}
	}
	return p, nil
}

// Dir is .atm/ in the project root, the only place ATM writes besides init's .atm.yaml and .gitignore.
const Dir = ".atm"

// Path is name in the project root.
func (p Project) Path(name string) string { return filepath.Join(p.Root, name) }
