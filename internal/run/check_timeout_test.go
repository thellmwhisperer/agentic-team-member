package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestChecksUseConfiguredTimeout(t *testing.T) {
	for _, step := range []string{"install", "test", "typecheck", "lint", "red", "green"} {
		t.Run(step, func(t *testing.T) {
			root := repo(t, "local", atmYAML+"check_timeout: 2s\n")
			c, err := config.Load(root, t.TempDir(), config.Agent{})
			if err != nil {
				t.Fatal(err)
			}
			if err := before(root); err != nil {
				t.Fatal(err)
			}
			sha := gitT(t, root, "rev-parse", "HEAD")
			// Bound a broken implementation too, and prove the configured limit can be longer.
			old := timeout
			timeout = time.Second
			t.Cleanup(func() { timeout = old })
			hang := "while :; do :; done"
			typ, test := "chore", ""
			switch step {
			case "install":
				c.Install = hang
			case "test":
				c.Test = hang
			case "typecheck":
				c.Typecheck = hang
			case "lint":
				c.Lint = hang
			case "red", "green":
				typ, test = "fix", "a_test.sh"
				body := hang
				if step == "green" {
					body = "grep -q fixed a.txt || exit 1; " + hang
				}
				for name, body := range map[string]string{"a.txt": "fixed\n", test: body} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			start := time.Now()
			_, err = checks(root, t.TempDir(), sha, typ, test, false, c)
			if !errors.Is(err, errTimeout) || !strings.Contains(err.Error(), "timed out after "+Human(2*time.Second)) {
				t.Fatalf("want configured timeout, got %v", err)
			}
			if time.Since(start) < 2*time.Second {
				t.Fatal("command stopped before its configured limit")
			}
		})
	}
}

func TestChecksDefaultTimeoutIsTenMinutes(t *testing.T) {
	if timeout != 10*time.Minute {
		t.Fatalf("default timeout = %s, want 10m", timeout)
	}
}
