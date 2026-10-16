package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run refuses, before it clones, an install the reach guard would reject if the worker ran it from its
// clone, and one that stays inside the clone starts as today.
func TestRunRefusesInstallReachingOutsideTheClone(t *testing.T) {
	repo(t, "https://github.com/owner/repo", atmInstall("cp /abs/outside/x .tmp/x"))
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "install reaches outside the clone: cp /abs/outside/x .tmp/x") {
		t.Fatalf("want the install reach refusal, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no step should start: %s", out.String())
	}
}

func TestRunTakesInstallInsideTheClone(t *testing.T) {
	root := repo(t, "https://github.com/owner/repo", atmInstall("scripts/slopslint.sh --version"))
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "slopslint.sh"), []byte("#!/bin/sh\nexit 0\n"),
		0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", "scripts")
	gitT(t, root, "commit", "-q", "-m", "slopslint")
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
}
