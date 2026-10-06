package run

import (
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type packageJSON struct {
	PackageManager  string         `json:"packageManager"`
	Scripts         map[string]any `json:"scripts"`
	Dependencies    map[string]any `json:"dependencies"`
	DevDependencies map[string]any `json:"devDependencies"`
}

// readPackage is the clone's package.json; ok is false when there is none.
func readPackage(clone string) (pkg packageJSON, ok bool) {
	b, err := os.ReadFile(filepath.Join(clone, "package.json"))
	return pkg, err == nil && json.Unmarshal(b, &pkg) == nil
}

func exists(clone, name string) bool {
	_, err := os.Stat(filepath.Join(clone, name))
	return err == nil
}

// language is the project's language, the key of its forbidden patterns.
func language(clone string) string {
	switch {
	case exists(clone, "go.mod"):
		return "go"
	case exists(clone, "package.json"):
		return "typescript"
	case exists(clone, "pyproject.toml") || exists(clone, "requirements.txt"):
		return "python"
	}
	return ""
}

// suite is the full test and typecheck commands the brief names: the configured ones, else detected.
func suite(clone, test, typecheck string) (string, string) {
	var t, c string
	switch language(clone) {
	case "go":
		t, c = "go test ./...", "go vet ./..."
	case "typescript":
		pkg, _ := readPackage(clone)
		pm := packageManager(clone, pkg)
		if pkg.Scripts["test"] != nil {
			t = pm + " run test"
		}
		if pkg.Scripts["typecheck"] != nil {
			c = pm + " run typecheck"
		}
	case "python":
		t = "python3 -m pytest"
	}
	return cmp.Or(test, t), cmp.Or(typecheck, c)
}

func packageManager(clone string, pkg packageJSON) string {
	for lock, pm := range map[string]string{"bun.lockb": "bun", "bun.lock": "bun", "pnpm-lock.yaml": "pnpm",
		"yarn.lock": "yarn", "package-lock.json": "npm"} {
		if exists(clone, lock) {
			return pm
		}
	}
	if name, _, _ := strings.Cut(pkg.PackageManager, "@"); name == "bun" || name == "pnpm" || name == "yarn" {
		return name
	}
	return "npm"
}

// jsRunner is the argv that runs one JavaScript or TypeScript test file, without the file; nil when the
// runner is not detected.
// ponytail: detected from package.json's dependencies only; today's runner_bootstrap also reads
// scripts.test and generates a next/jest config shim. Port those when a target needs them.
func jsRunner(clone string) []string {
	pkg, _ := readPackage(clone)
	pm, deps := packageManager(clone, pkg), func(name string) bool {
		return pkg.Dependencies[name] != nil || pkg.DevDependencies[name] != nil
	}
	exec := map[string][]string{"pnpm": {"pnpm", "exec"}, "yarn": {"yarn"}, "bun": {"bun", "x"}}[pm]
	if exec == nil {
		exec = []string{"npm", "exec", "--"}
	}
	vitest, jest := deps("vitest"), deps("jest") || deps("@jest/globals")
	switch {
	case vitest && !jest:
		return append(exec, "vitest", "run")
	case jest && !vitest:
		return append(exec, "jest", "--runInBand", "--watchman=false", "--coverage=false")
	case !vitest && !jest && (deps("bun-types") || deps("@types/bun") || pm == "bun"):
		return []string{"bun", "test"}
	}
	return nil
}

// testArgv runs the one test file rel: pytest for Python, the detected runner for JavaScript, configured
// otherwise, which may place the file itself with {test_file} or {test_dir} (Go tests run by package).
func testArgv(clone, rel, configured string) ([]string, error) {
	js := slices.Contains([]string{".ts", ".tsx", ".js", ".jsx"}, path.Ext(rel))
	switch {
	case path.Ext(rel) == ".py":
		return []string{"python3", "-m", "pytest", rel}, nil
	case js && jsRunner(clone) != nil:
		return append(jsRunner(clone), rel), nil
	case js && configured == "":
		return []string{"bun", "test", rel}, nil
	case configured == "":
		return nil, errors.New("no runner for " + rel + ": set commands.test in .atm.yaml")
	}
	dir := "./" + path.Dir(rel)
	if dir == "./." {
		dir = "./"
	}
	line := strings.NewReplacer("{test_file}", shellQuote(rel), "{test_dir}", shellQuote(dir)).Replace(configured)
	if line == configured {
		line += " " + shellQuote(rel)
	}
	return []string{"sh", "-c", line}, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
