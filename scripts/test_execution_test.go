package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTestScriptExecution(t *testing.T) {
	for _, mode := range []struct {
		name    string
		shards  string
		wantRun int
		fail    []string
	}{
		{"sequential", "1", 0, []string{"", "./..."}},
		{"default", "", 8, []string{"", "./internal/run", "example.com/atm/scripts"}},
	} {
		for _, fail := range mode.fail {
			t.Run(mode.name+"/fail="+fail, func(t *testing.T) {
				dir := t.TempDir()
				log := filepath.Join(dir, "calls")
				if err := os.Mkdir(log, 0o755); err != nil {
					t.Fatal(err)
				}
				fake := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$FAKE_GO_LOG/$$"
case "$*" in
  'list ./...')
    printf '%s\n' example.com/atm/internal/run example.com/atm/scripts
    exit 0 ;;
  'test -list . ./internal/run')
    printf '%s\n' TestAlpha TestBeta
    exit 0 ;;
esac
[ "$1" = test ] || exit 2
for arg do
  if [ "$arg" = "$FAKE_GO_FAIL" ]; then exit 1; fi
done
`
				if err := os.WriteFile(filepath.Join(dir, "go"), []byte(fake), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("ATM_TEST_SHARDS", mode.shards)
				t.Setenv("FAKE_GO_LOG", filepath.ToSlash(log))
				t.Setenv("FAKE_GO_FAIL", fail)
				cmd := exec.Command("sh", "test.sh")
				out, err := cmd.CombinedOutput()
				if err != nil {
					if _, ok := err.(*exec.ExitError); !ok {
						t.Fatalf("start test.sh: %v", err)
					}
				}
				if (err != nil) != (fail != "") {
					t.Fatalf("test.sh: %v, want failure=%t\n%s", err, fail != "", out)
				}
				calls, err := os.ReadDir(log)
				if err != nil {
					t.Fatal(err)
				}
				var runs, rest, sequential int
				for _, call := range calls {
					data, err := os.ReadFile(filepath.Join(log, call.Name()))
					if err != nil {
						t.Fatal(err)
					}
					args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
					if args[0] != "test" || slices.Contains(args, "-list") {
						continue
					}
					if i := slices.Index(args, "-timeout"); i < 0 || i+1 >= len(args) || args[i+1] != "30m" {
						t.Errorf("go %q: want -timeout 30m on every test process", args)
					}
					switch args[len(args)-1] {
					case "./internal/run":
						runs++
					case "example.com/atm/scripts":
						rest++
					case "./...":
						sequential++
					default:
						t.Errorf("unexpected test arguments: %q", args)
					}
				}
				if mode.shards == "1" {
					if sequential != 1 || runs != 0 || rest != 0 {
						t.Fatalf("sequential=%d, run=%d, rest=%d; want 1, 0, 0", sequential, runs, rest)
					}
				} else if runs != mode.wantRun || rest != 1 || sequential != 0 {
					t.Fatalf("run=%d, rest=%d, sequential=%d; want %d, 1, 0", runs, rest, sequential, mode.wantRun)
				}
			})
		}
	}
}
