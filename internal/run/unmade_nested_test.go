package run

import "testing"

// The files of an ignored nested repository the agent rewrote, its HEAD left alone, are back as before recorded
// them when install runs: whole sees the repository as one gitlink, which did not change.
func TestInstallSeesIgnoredNestedRepositoryAsBeforeRecordedIt(t *testing.T) {
	_, err := unmadeChecks(t, nested, "echo agent > cache/nested/untracked.txt && echo agent > cache/nested/tracked.txt",
		nestedKept, false)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
}
