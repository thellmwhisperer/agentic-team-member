package run

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputEventNamesItsCommand(t *testing.T) {
	atm := atmYAML
	for _, kv := range [][2]string{{"install", "echo out-install"}, {"test", "echo out-test; sh a_test.sh"},
		{"typecheck", "echo out-typecheck"}, {"lint", "echo out-lint"},
		{"test_file", "echo out-red/green; sh {file}"}, {"delivery", "echo out-delivery"}} {
		atm = atmSet(atm, kv[0], kv[1])
	}
	repo(t, t.TempDir(), atm)
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	var w watched
	if err := Run("t", []string{issueFile(t, issue)}, &w, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, ev := range w.live {
		out, ok := ev["output"].(string)
		if !ok || ev["step"] != "checks" && ev["step"] != "delivery" {
			continue
		}
		check, _ := ev["check"].(string)
		if name, ok := strings.CutPrefix(out, "out-"); ok {
			if check != name {
				t.Errorf("output of %s names %q: %v", name, check, ev)
			}
			named[name] = true
		} else if check == "" {
			t.Errorf("output names no command: %v", ev)
		}
	}
	for _, name := range []string{"install", "red/green", "test", "typecheck", "lint", "delivery"} {
		if !named[name] {
			t.Errorf("no output event of %s in %v", name, w.live)
		}
	}
}
