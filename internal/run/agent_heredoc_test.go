package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReachAcceptsQuotedHeredocTestData(t *testing.T) {
	// Recorded Bash input from 311-72, 2026-10-13T11:56:28.964Z. The URL is
	// literal Go fixture data, not a shell argument or an executed request.
	command := fixture(t, "quoted-heredoc-311.sh")
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	target := filepath.Join("internal", "run", "followup_test.go")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, target)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("package run\nimport \"testing\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", target)
	gitT(t, root, "commit", "-q", "-m", "add existing test file")
	crlf := bytes.ReplaceAll([]byte(command), []byte("\r\n"), []byte("\n"))
	crlf = bytes.ReplaceAll(crlf, []byte("\n"), []byte("\r\n"))
	for _, input := range []struct {
		name, command string
	}{
		{"LF", command},
		{"CRLF", string(crlf)},
	} {
		t.Run(input.name, func(t *testing.T) {
			if fetchesAt(input.command, root) {
				t.Error("literal fixture heredoc classified as a network fetch")
			}
			if got := outside([]use{{"Bash", input.command, input.command}}, root, root); got != "" {
				t.Fatalf("literal fixture heredoc reached outside clone: %s", got)
			}
		})
	}
	t.Setenv("FAKE_AGENT_STREAM", bashEvent("claude", command, ""))
	var out bytes.Buffer
	if err := Run("t", []string{"--harness", "claude", issueFile(t, issue)}, &out, io.Discard); err != nil {
		t.Fatalf("recorded fixture command failed the fake-agent boundary: %v", err)
	}
}

func TestReachRejectsExecutingWrittenHeredoc(t *testing.T) {
	for _, after := range []string{"sh payload.sh", "bash ./payload.sh", ". payload.sh", "source payload.sh",
		"chmod +x payload.sh; ./payload.sh", "mv payload.sh other.sh; sh other.sh", "sh -c 'sh payload.sh'",
		"script=payload.sh; sh \"$script\"", "chmod +x payload.*; ./payload.*", "chmod +x payload?sh; ./payload?sh",
		"chmod +x payload[.]sh; ./payload[.]sh", "chmod +x payload.{sh,txt}; ./payload.{sh,txt}",
		"shopt -s nocaseglob; chmod +x PAYLOAD.*; ./PAYLOAD.*",
		"shopt -s globstar; chmod +x **/payload.sh; **/payload.sh"} {
		command := "cat > payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" + after
		if !fetches(command) {
			t.Errorf("written executable escaped reach check: %s", after)
		}
	}
	for _, after := range []string{"cd sub; chmod +x payload.sh; ./payload.sh", "cd sub; chmod +x payload.*; ./payload.*",
		"if cd sub; then chmod +x payload.sh; ./payload.sh; fi"} {
		command := "cat > sub/payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" + after
		if !fetches(command) {
			t.Errorf("directory change hid executable heredoc: %s", after)
		}
	}
	for _, after := range []string{"eval 'cd sub'; chmod +x payload.sh; ./payload.sh",
		"if eval 'cd sub'; then chmod +x payload.sh; ./payload.sh; fi"} {
		command := "cat > sub/payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" + after
		if !fetches(command) {
			t.Errorf("eval hid executable heredoc: %s", after)
		}
	}
}

func TestReachAcceptsQuotedGoTestSource(t *testing.T) {
	source := "package run\nimport \"testing\"\n" +
		"func TestFixture(t *testing.T) { t.Log(\"https://example.com/repo.git\") }\n"
	for _, after := range []string{"", "go test ./internal/run -run 'TestOne|TestTwo' -count=1",
		"go test ./internal/run -run 'TestOne|TestTwo' -count=1 2>&1 | tail -20", "go test '~'"} {
		command := "cat > fixture_test.go <<'EOF'\n" + source + "EOF\n" + after
		if fetches(command) {
			t.Errorf("literal Go test fixture rejected: %s", after)
		}
		if got := outside([]use{{"Bash", command, command}}, "/repo", "/repo/clone"); got != "" {
			t.Errorf("literal Go test fixture reached outside clone: %s", got)
		}
	}
	quotedOutput := "cat > 'fixture[.]_test.go' <<'EOF'\n" + source + "EOF\n"
	if fetches(quotedOutput) {
		t.Fatal("quoted output metacharacters were treated as shell expansion")
	}
	quotedTildeOutput := "cat > '~/fixture_test.go' <<'EOF'\n" + source + "EOF\n"
	if fetches(quotedTildeOutput) {
		t.Fatal("quoted tilde output was treated as shell expansion")
	}
	tildeOutput := "cat > ~/fixture_test.go <<'EOF'\n" + source + "EOF\n"
	if !fetches(tildeOutput) {
		t.Fatal("unquoted tilde output was treated as a clone-relative path")
	}
	comment := "cat > fixture_test.go <<'EOF'\npackage run\nimport \"testing\"\n// https://example.com/repo.git\n" +
		"func TestFixture(t *testing.T) {}\nEOF\n"
	if fetches(comment) {
		t.Fatal("URL in a Go comment was classified as a shell request")
	}
}

func TestReachRejectsUnprovenHeredocWriter(t *testing.T) {
	for _, destination := range []string{"payload.sh", "payload.txt", "payload_test.go", "../outside_test.go"} {
		write := "cat > " + destination + " <<'EOF'\ncurl https://example.com\nEOF\n"
		if !fetches(write) {
			t.Errorf("unproven URL-bearing writer accepted: %s", destination)
		}
		calls := []use{{"Bash", write, write}, {"Bash", "sh " + destination, "sh " + destination}}
		if got := outside(calls, "/repo", "/repo/clone"); got != "Bash "+write {
			t.Errorf("want rejection at the writer for %s, got %q", destination, got)
		}
	}
	valid := "package run\nimport \"testing\"\n" +
		"func TestFixture(t *testing.T) { t.Log(\"https://example.com/repo.git\") }\n"
	for _, tc := range []struct{ name, prefix, body, after string }{
		{"renamed shell", "", "curl https://example.com", ""},
		{"unknown continuation", "", valid, "sh fixture_test.go"},
		{"nonliteral test command", "", valid, "go test \"$ARGS\""},
		{"not a leading writer", "echo ready; ", valid, "go test ./..."},
		{"invalid Go source", "", "package run\ncurl https://example.com\n", ""},
		{"tilde test argument expansion", "", valid, "go test ~/..."},
		{"ignored Testimony helper", "", "package run\nfunc Testimony() { _ = \"https://example.com\" }\n", ""},
		{"test missing testing parameter", "", "package run\nfunc TestNoParam() { _ = \"https://example.com\" }\n", ""},
		{"test with wrong parameter type", "", "package run\n" +
			"func TestWrong(t testing.T) { _ = \"https://example.com\" }\n", ""},
		{"test with result", "", "package run\nimport \"testing\"\n" +
			"func TestResult(t *testing.T) bool { _ = \"https://example.com\"; return true }\n", ""},
		{"custom test parameter", "", "package run\nimport \"custom\"\n" +
			"func TestCustom(t *custom.T) { _ = \"https://example.com\" }\n", ""},
		{"bare test parameter", "", "package run\nimport \"testing\"\ntype T struct{}\n" +
			"func TestBare(t *T) { _ = \"https://example.com\" }\n", ""},
		{"missing testing import", "", "package run\nfunc TestMissing(t *testing.T) { _ = \"https://example.com\" }\n", ""},
		{"aliased testing import", "", "package run\nimport test \"testing\"\n" +
			"func TestAlias(t *test.T) { _ = \"https://example.com\" }\n", ""},
	} {
		write := tc.prefix + "cat > fixture_test.go <<'EOF'\n" + tc.body + "EOF\n" + tc.after
		if !fetches(write) {
			t.Errorf("%s accepted without the Go test source contract", tc.name)
		}
	}
}

func TestReachQuotedHeredocKeepsExecutableInputChecked(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		fetch         bool
	}{
		{"single quoted non-source payload", "cat > fixture.txt <<'EOF'\nhttps://example.com/repo.git\nEOF\n", true},
		{"double quoted non-source payload", "cat >> fixture.txt <<\"EOF\"\nhttps://example.com/repo.git\nEOF\n", true},
		{"quoted output metacharacters do not make text Go source",
			"cat > 'fixture[.]txt' <<'EOF'\nhttps://example.com/repo.git\nEOF\n", true},
		{"glob output path may overwrite executable", "cat > payload[.]sh <<'EOF'\nhttps://example.com\nEOF\n" +
			"chmod +x payload.sh; ./payload.sh", true},
		{"brace output path may overwrite executable", "cat > payload.{sh,txt} <<'EOF'\nhttps://example.com\nEOF\n" +
			"chmod +x payload.sh; ./payload.sh", true},
		{"unrelated glob conservatively retains the heredoc URL",
			"cat > fixture.txt <<'EOF'\nhttps://example.com/repo.git\nEOF\nls *.go", true},
		{"quoted glob with unknown continuation",
			"cat > fixture_test.go <<'EOF'\npackage p\n" +
				"func TestX() { _ = \"https://example.com\" }\nEOF\nprintf '%s' 'payload.*'", true},
		{"quoted brace with unknown continuation",
			"cat > fixture_test.go <<'EOF'\npackage p\n" +
				"func TestX() { _ = \"https://example.com\" }\nEOF\nprintf '%s' 'payload.{sh,txt}'", true},
		{"unquoted expansion", "cat > fixture.txt <<EOF\n$(curl \"$URL\")\nEOF\n", true},
		{"literal shell substitution text", "cat > fixture.txt <<'EOF'\n$(curl https://example.com)\nEOF\n", true},
		{"eval after data", "cat > fixture.txt <<'EOF'\nhttps://example.com/repo.git\nEOF\neval 'cd sub'", true},
		{"network after data", "cat > fixture.txt <<'EOF'\nhttps://example.com\nEOF\ncurl \"$URL\"", true},
		{"url after data", "cat > fixture.txt <<'EOF'\nhttps://example.com\nEOF\n" +
			"node -e \"fetch('https://remote.example')\"", true},
		{"written executable with repeated slash", "cat > sub/payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" +
			"chmod +x sub//payload.sh; ./sub//payload.sh", true},
		{"written executable with dot segment", "cat > sub/payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" +
			"chmod +x sub/./payload.sh; ./sub/./payload.sh", true},
		{"written executable with parent segment", "cat > sub/payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" +
			"chmod +x sub/../sub/payload.sh; ./sub/../sub/payload.sh", true},
		{"executable heredoc", "sh <<'EOF'\ncurl https://example.com\nEOF\n", true},
		{"bash executable heredoc", "bash <<'EOF'\nwget https://example.com\nEOF\n", true},
		{"piped executable heredoc", "cat <<'EOF' | sh\ncurl https://example.com\nEOF\n", true},
		{"descriptor pseudo-file", "cat > /dev/stdout <<'EOF' | sh\ncurl https://example.com\nEOF\n", true},
		{"later descriptor override", "cat > fixture.txt >&1 <<'EOF' | sh\ncurl https://example.com\nEOF\n", true},
		{"nested shell", "sh -c \"curl https://example.com\"", true},
		{"redefined cat", "cat() { sh; }; cat > fixture.txt <<'EOF'\ncurl https://example.com\nEOF\n", true},
		{"alias before cat", "alias cat=sh\ncat > fixture.txt <<'EOF'\ncurl https://example.com\nEOF\n", true},
		{"nested cat pipeline", "cat > fixture.txt <<'EOF' | sh\ncurl https://example.com\nEOF\n", true},
		{"output command substitution", "cat >$(curl https://example.com) <<'EOF'\ndata\nEOF\n", true},
		{"url output filename", "cat > 'https://example.com' <<'EOF'\ndata\nEOF\n", true},
		{"invalid shell stays conservative", "cat > fixture.txt <<'EOF'\nhttps://example.com\n", true},
	} {
		if got := fetches(tc.command); got != tc.fetch {
			t.Errorf("%s: fetches = %v, want %v: %s", tc.name, got, tc.fetch, tc.command)
		}
	}
	command := "cat > /repo/outside.txt <<'EOF'\nhttps://example.com\nEOF\n"
	if got := outside([]use{{"Bash", command, command}}, "/repo", "/repo/clone"); got == "" {
		t.Fatal("outside-clone destination was accepted")
	}
}
