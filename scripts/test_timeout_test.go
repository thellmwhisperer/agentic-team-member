package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestGlobalTestTimeoutIsThirtyMinutes(t *testing.T) {
	for _, path := range []string{"Makefile", "scripts/test.sh"} {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", path))
			if err != nil {
				t.Fatal(err)
			}
			match := regexp.MustCompile(`(?m)^\s*go test\s+-timeout(?:=|\s+)(\S+)\s+\./\.\.\.\s*$`).
				FindSubmatch(b)
			if match == nil {
				t.Fatal("missing explicit timeout on the complete Go suite")
			}
			limit, err := time.ParseDuration(string(match[1]))
			if err != nil || limit != 30*time.Minute {
				t.Fatalf("global test timeout = %q, %v; want 30m", match[1], err)
			}
		})
	}
}
