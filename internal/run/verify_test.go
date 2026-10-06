package run

import "testing"

func TestMissingModuleSkipsPytestCapturedOutput(t *testing.T) {
	failed := "E   ModuleNotFoundError: No module named 'calc2'\n"
	captured := "----- Captured stdout call -----\nModuleNotFoundError: No module named 'echoed'\n" +
		"===== short test summary info =====\nFAILED tests/test_add.py::test_add - assert -1 == 5\n"
	for output, want := range map[string]string{failed: "no module named 'calc2'", captured: ""} {
		if got := missingModule(output); got != want {
			t.Errorf("missingModule(%q) = %q, want %q", output, got, want)
		}
	}
}
