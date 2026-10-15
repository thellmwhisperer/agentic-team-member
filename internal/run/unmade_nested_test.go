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

// The latest before is the oracle: removing a nested repository after that
// snapshot must restore its files; removing it before must not resurrect them.
func TestUnmadeWithNoRemainingNestedRepositories(t *testing.T) {
	for _, recapture := range []bool{false, true} {
		name := "deleted after before"
		if recapture {
			name = "deleted before repeated before"
		}
		t.Run(name, func(t *testing.T) {
			dir := repo(t, "https://example.com/owner/repo.git", atmYAML)
			commitBase(t, dir, ".gitignore", "cache/\n")
			if _, err := sh(dir, nested+" && chmod 755 ."); err != nil {
				t.Fatal(err)
			}
			if err := before(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := sh(dir, "rm -rf cache/nested"); err != nil {
				t.Fatal(err)
			}
			if recapture {
				if err := before(dir); err != nil {
					t.Fatal(err)
				}
			}
			if err := unmade(dir); err != nil {
				t.Fatal(err)
			}
			want := `test "$(cat cache/nested/tracked.txt)" = tracked && test "$(cat cache/nested/untracked.txt)" = untracked`
			if recapture {
				want = "test ! -e cache/nested"
			}
			if _, err := sh(dir, want); err != nil {
				t.Fatal(err)
			}
		})
	}
}
