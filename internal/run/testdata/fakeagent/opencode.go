package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openCodeAPI plays `opencode api --standalone method` of OpenCode 2: it prints $FAKE_OPENCODE_API/<method>.json,
// a recorded answer, <clone> in it its working directory. Run outside a run's clone, or with no recorded answer,
// it fails, as a test of the worker's directory and environment.
func openCodeAPI(method string) int {
	wd, _ := os.Getwd()
	if !strings.Contains(filepath.ToSlash(wd), "/.atm/clones/") {
		fmt.Fprintln(os.Stderr, "opencode api: not in the worker's clone:", wd)
		return 1
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_OPENCODE_API"), method+".json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(strings.ReplaceAll(string(b), "<clone>", filepath.ToSlash(wd)))
	return 0
}
