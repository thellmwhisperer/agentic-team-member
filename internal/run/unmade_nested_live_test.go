package run

import (
	"os"
	"path/filepath"
	"testing"
)

// The oracle is the pre-agent bytes and a copy of the nested repository's Git
// data. Check them inside install, before a later reset could hide a failure.
func TestChecksNestedInstallPreservesBytesAndGitData(t *testing.T) {
	for _, mutation := range []struct{ name, work string }{
		{"rewrite", "echo agent > cache/nested/tracked.txt && echo agent > cache/nested/untracked.txt && " +
			"echo agent > 'cache/nested/sub/state [x]'"},
		{"delete", "rm cache/nested/tracked.txt cache/nested/untracked.txt"},
		{"symlink", `rm cache/nested/tracked.txt cache/nested/untracked.txt && ` +
			`ln -s "$EXT/sentinel" cache/nested/tracked.txt && ln -s "$EXT/sentinel" cache/nested/untracked.txt`},
		{"deleted subdirectory", "rm -r cache/nested/sub"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			old := "(" + nested + ") && chmod 755 cache/nested && mkdir cache/nested/sub && " +
				`printf 'old\r\n\000\377\n' > 'cache/nested/sub/state [x]' && ` +
				`cp 'cache/nested/sub/state [x]' "$EXT/expected" && ` +
				`echo sentinel > "$EXT/sentinel" && cp -R cache/nested/.git "$EXT/git-before" && chmod 555 cache/nested`
			work := "chmod 755 cache/nested && " + mutation.work +
				" && echo debris > cache/nested/new-helper && chmod 555 cache/nested"
			kept := `diff -r "$EXT/git-before" cache/nested/.git && ` +
				`is cache/nested/tracked.txt tracked && is cache/nested/untracked.txt untracked && ` +
				`test ! -L cache/nested/tracked.txt && test ! -L cache/nested/untracked.txt && ` +
				`cmp "$EXT/expected" 'cache/nested/sub/state [x]' && is "$EXT/sentinel" sentinel && ` +
				`ro cache/nested && test ! -e cache/nested/new-helper && ` +
				`{ printf 'install observed tracked='; cat cache/nested/tracked.txt; ` +
				`printf 'install observed untracked='; cat cache/nested/untracked.txt; ` +
				`printf 'binary bytes='; od -An -tx1 'cache/nested/sub/state [x]'; ` +
				`printf 'Git data unchanged; directory mode 555; agent helper absent; link target untouched\n'; ` +
				`} > "$EXT/observation"`
			_, err := unmadeChecks(t, old, work, kept, false)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(os.Getenv("EXT"), "observation"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s", b)
		})
	}
}
