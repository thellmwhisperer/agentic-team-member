package run

import (
	"os"
	"testing"
)

func writeFixture(t *testing.T, path, content string, modes ...os.FileMode) {
	t.Helper()
	mode := os.FileMode(0o600)
	if len(modes) > 0 {
		mode = modes[0]
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
