package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// selectors are Config's leaf keys as Go selectors: .Harness, .Commands.Test, ...
func selectors(t reflect.Type, parent string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Struct {
			out = append(out, selectors(f.Type, parent+"."+f.Name)...)
		} else {
			out = append(out, parent+"."+f.Name)
		}
	}
	return out
}

// A key is read when some non-test Go file outside this package names its selector, as
// tests/test_config.py asks of the Python config.
func TestEveryKeyIsRead(t *testing.T) {
	var sources []string
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") && path != "../.." || d.Name() == "config") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			b, err := os.ReadFile(path)
			sources = append(sources, string(b))
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range selectors(reflect.TypeFor[Config](), "") {
		if !strings.Contains(strings.Join(sources, "\n"), sel) {
			t.Errorf("nobody reads the key %s", sel)
		}
	}
}

func TestLayersOverrideInOrder(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write(t, filepath.Join(home, ".config", "atm", "config.yaml"), "harness: codex\nmodel: global\neffort: low\n")
	write(t, filepath.Join(root, ProjectFile), "model: project\ncommands:\n  test: make test\n")
	flags := pflag.NewFlagSet("atm", pflag.ContinueOnError)
	AddFlags(flags)
	if err := flags.Parse([]string{"--effort", "high"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root, flags)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Harness, want.Model, want.Effort, want.Commands.Test = "codex", "project", "high", "make test"
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Load = %+v, want %+v", c, want)
	}
}

func TestLoadRejects(t *testing.T) {
	for _, text := range []string{"modle: typo\n", "harness: cursor\n", "effort: extreme\n", "timeouts:\n  agent: 0\n"} {
		root := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		write(t, filepath.Join(root, ProjectFile), text)
		if _, err := Load(root, pflag.NewFlagSet("atm", pflag.ContinueOnError)); err == nil {
			t.Errorf("Load accepts %q", text)
		}
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
