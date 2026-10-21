package checks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

const twoSubtests = "package a\n\nfunc TestA(t *testing.T) {\n\tt.Run(\"one\", func(t *testing.T) {})\n" +
	"\tt.Run(\"two\", func(t *testing.T) {})\n}\n"

// TestChecksFailWhenATestFileLosesTests: a clone whose diff deletes a t.Run subtest from a test file fails
// Checks naming the file, unless the issue names it; adapting the subtest instead passes (decision 8087).
func TestChecksFailWhenATestFileLosesTests(t *testing.T) {
	for _, c := range []struct{ name, work, issue, why string }{
		{name: "deleted", work: "package a\n\nfunc TestA(t *testing.T) {\n\tt.Run(\"one\", func(t *testing.T) {})\n}\n",
			why: "the test file a_test.go loses tests: 3 before, 2 after"},
		{name: "deleted and named", issue: "Remove the subtest `two`.",
			work: "package a\n\nfunc TestA(t *testing.T) {\n\tt.Run(\"one\", func(t *testing.T) {})\n}\n"},
		{name: "adapted", work: strings.Replace(twoSubtests, `"two", func(t *testing.T) {}`,
			`"two", func(t *testing.T) { t.Log("adapted") }`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			clone := t.TempDir()
			gitT := func(args ...string) (string, error) {
				out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput()
				return strings.TrimSpace(string(out)), err
			}
			write := func(text string) {
				if err := os.WriteFile(filepath.Join(clone, "a_test.go"), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(twoSubtests)
			for _, args := range [][]string{{"init", "-q"}, {"add", "-A"},
				{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base"}} {
				if out, err := gitT(args...); err != nil {
					t.Fatal(out, err)
				}
			}
			sha, _ := gitT("rev-parse", "HEAD")
			write(c.work)
			_, err := Run(clone, t.TempDir(), sha, sha, "", &c.issue, false,
				config.Config{TestPatterns: []string{"**/*_test.go"}}, Tools[string]{
					Git:     func(_ string, args ...string) (string, error) { return gitT(args...) },
					Moved:   func(string, string, string) error { return nil },
					Unmade:  func(string) error { return nil },
					Command: func(string, string, ...string) (string, error) { return "", nil },
				})
			if c.why == "" && err != nil || c.why != "" && (err == nil || err.Error() != c.why) {
				t.Fatalf("want %q, got %v", c.why, err)
			}
		})
	}
}
