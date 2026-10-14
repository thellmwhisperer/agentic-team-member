package run

import "testing"

// An ignored file the agent rewrote is back as before recorded it when install runs, so install sees the
// dependency the agent added as missing and makes it again: a package manager's state, pnpm's lock.yaml say.
func TestInstallSeesIgnoredFilesAsBeforeRecordedThem(t *testing.T) {
	_, err := unmadeChecks(t, "echo dep/ >> .git/info/exclude && mkdir dep && echo old > dep/state",
		`echo new > dep/state && touch dep/link && echo fixed > a.txt && `+
			`echo 'test -e dep/link && test "$(cat a.txt)" = fixed' > a_test.sh`,
		"{ grep -q old dep/state && touch dep/link || true; }", true)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
}
