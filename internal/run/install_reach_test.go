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
	root := repo(t, "https://github.com/owner/repo", "")
	install := "cp " + filepath.ToSlash(gitT(t, root, "rev-parse", "--show-toplevel")) + "/.tmp/slopslint/x .tmp/x"
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atmInstall(install)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "install reaches outside the clone: "+install) {
		t.Fatalf("want the install reach refusal, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no step should start: %s", out.String())
	}
}

func TestRunTakesInstallExecutableOutsideRepository(t *testing.T) {
	repo(t, "https://github.com/owner/repo", atmInstall("/usr/local/go/bin/go mod download"))
	var out bytes.Buffer
	err := Run("t", []string{"--base-ref", "missing-install-test-base", issueFile(t, issue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `base ref "missing-install-test-base"`) {
		t.Fatalf("want install accepted and clone attempted, got %v", err)
	}
	if end := cloneStep(t, &out); end["state"] != "failed" {
		t.Fatalf("want clone to fail on missing base, got %v", end)
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
