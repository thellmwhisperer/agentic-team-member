package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	addTest    = "from calc import add\n\n\ndef test_add_sums():\n    assert add(2, 3) == 5\n"
	add2Test   = "from calc2 import add\n\n\ndef test_add_sums():\n    assert add(2, 3) == 5\n"
	mulNegTest = "from calc import mul\n\n\ndef test_mul_negative():\n    assert mul(-1, 1) == -1\n"
	unused     = "\n\ndef unused(x):\n    if x is None:\n        return 0\n    return x\n"
	// criterion is a sentence of the e2e issue, so the follow-up that names it is chained as unit 2.
	criterion = "add(2, 3) returns -1 instead of 5."
)

// play does what the scenario's agent does in the working directory and returns its last message.
func play(scenario, brief string) (string, error) {
	if strings.HasPrefix(brief, "# Ponytail pass") {
		return ponytail(scenario)
	}
	if scenario == "follow-up" && strings.Contains(brief, "# Follow-up unit 2") {
		return fixMul()
	}
	files, ok := scenarios[scenario]
	if !ok {
		return "", fmt.Errorf("unknown scenario %q", scenario)
	}
	if scenario == "sleeping" {
		return sleep()
	}
	for _, f := range files {
		if err := write(f[0], f[1]); err != nil {
			return "", err
		}
	}
	if scenario == "helper-only" {
		return "I added a helper.", nil
	}
	tail := ""
	if scenario == "ponytail-cuts" {
		tail = unused
	}
	fix := func(s string) string { return strings.Replace(s, "a - b", "a + b", 1) + tail }
	if err := edit("calc.py", fix); err != nil {
		return "", err
	}
	if scenario == "new-module" {
		return report("tests/test_add2.py", []string{"calc.py", "calc2.py", "tests/test_add2.py"}, nil)
	}
	var followUps []obj
	if scenario == "follow-up" {
		followUps = []obj{{"title": "mul drops the sign", "paths": []string{"calc.py"},
			"red_test": "tests/test_mul_neg.py", "criterion": criterion}}
	}
	return report("tests/test_add.py", []string{"calc.py", "tests/test_add.py"}, followUps)
}

// scenarios: the files each agent writes. Every one but helper-only and sleeping also fixes add in calc.py.
var scenarios = map[string][][2]string{
	"fixing":         {{"tests/test_add.py", addTest}},
	"ponytail-cuts":  {{"tests/test_add.py", addTest}},
	"scope-breaking": {{"tests/test_add.py", addTest}, {"other.py", "def other():\n    return 2\n"}},
	"helper-only":    {{"helpers.py", "def helper():\n    return 1\n"}},
	"new-module":     {{"calc2.py", "def add(a, b):\n    return a + b\n"}, {"tests/test_add2.py", add2Test}},
	"follow-up":      {{"tests/test_add.py", addTest}, {".atm/follow-ups/1/tests/test_mul_neg.py", mulNegTest}},
	"sleeping":       nil,
}

// ponytail cuts the unused function the ponytail-cuts agent wrote; every other scenario finds nothing to cut.
func ponytail(scenario string) (string, error) {
	if scenario != "ponytail-cuts" {
		return `{"findings": [], "summary": "No cuts found"}`, nil
	}
	if err := edit("calc.py", func(s string) string { return strings.Replace(s, unused, "", 1) }); err != nil {
		return "", err
	}
	return marshal(obj{"summary": "cut one function", "findings": []obj{{"file": "calc.py",
		"family": "speculative_feature", "finding": "unused() has no caller: deleted"}}})
}

// fixMul is unit 2 of the follow-up scenario: it fixes mul on top of unit 1, which must be in place.
func fixMul() (string, error) {
	calc, err := os.ReadFile("calc.py")
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(calc), "a + b") {
		return "", errors.New("unit 2 runs without unit 1's fix in calc.py")
	}
	if err := edit("calc.py", func(s string) string { return strings.Replace(s, "abs(a * b)", "a * b", 1) }); err != nil {
		return "", err
	}
	return report("tests/test_mul_neg.py", []string{"calc.py"}, nil)
}

// sleep never finishes, and a grandchild holds stdout open: only killing the process group ends the stream.
func sleep() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	grandchild := exec.Command(exe)
	grandchild.Env = append(os.Environ(), "FAKEAGENT_GRANDCHILD=1")
	grandchild.Stdout = os.Stdout
	if err := grandchild.Start(); err != nil {
		return "", err
	}
	time.Sleep(time.Minute)
	return "", errors.New("slept a minute and was never killed")
}

func report(testFile string, changed []string, followUps []obj) (string, error) {
	text, err := marshal(obj{"test_file": testFile, "changed_files": changed, "summary": "fixed",
		"commands_run": []string{}, "follow_ups": append([]obj{}, followUps...)})
	return "Done.\n" + text, err
}

func marshal(v obj) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func write(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func edit(path string, change func(string) string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return write(path, change(string(b)))
}
