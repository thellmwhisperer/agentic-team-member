//go:build unix

package run

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The real terminal must name a failed tool call in the Agent box while the agent is still working.
func TestRealScreenShowsAFailedToolCall(t *testing.T) {
	builtATM(t)
	top := atmRepo(t)
	barrier := filepath.Join(t.TempDir(), "agent")
	held(t, barrier)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"assistant","message":{"content":[`+
		`{"type":"tool_use","id":"failed-call","name":"Bash","input":{"command":"false"}}]}}`+"\n"+
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"failed-call",`+
		`"is_error":true,"content":"exit status 1"}]}}`+"\n")
	t.Setenv("FAKE_AGENT_WORK", fmt.Sprintf(`cat %q >/dev/null; `, barrier)+fixWork)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	f := startFollow(t, versionServer(t, top, "follow-test"), top, true, "run", issueFile(t, issue))
	var shown strings.Builder
	untilShown(t, f, &shown, "Agent")
	untilShown(t, f, &shown, "failed Bash false · exit status 1")
	free(t, barrier)
	f.exit(0)
}

// Checks must restore ignored files before install, including deletions and file-to-symlink changes.
func TestRealRunRestoresIgnoredFiles(t *testing.T) {
	builtATM(t)
	install := `if test -d cache; then test "$(cat cache/modified)" = before && ` +
		`test "$(cat cache/deleted)" = before && test ! -L cache/type && test "$(cat cache/type)" = before; ` +
		`else mkdir cache; for f in modified deleted type; do echo before > "cache/$f"; done; fi`
	top := repo(t, "local", atmInstall(install))
	writeFixture(t, filepath.Join(top, ".gitignore"), "cache/\n")
	gitT(t, top, "add", ".gitignore")
	gitT(t, top, "commit", "-qm", "ignore install state")
	t.Setenv("FAKE_AGENT_WORK", fixWork+`; echo changed > cache/modified; rm cache/deleted cache/type; `+
		`ln -s ../a.txt cache/type`)
	bin := versionServer(t, top, "follow-test")
	startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue)).exit(0)
}
