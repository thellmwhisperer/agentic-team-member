package run

import "testing"

func TestReachAcceptsQuotedHeredocTestData(t *testing.T) {
	// Recorded Bash input from 311-72, 2026-10-13T11:56:28.964Z. The URL is
	// literal Go fixture data, not a shell argument or an executed request.
	command := fixture(t, "quoted-heredoc-311.sh")
	if fetches(command) {
		t.Error("literal fixture heredoc classified as a network fetch")
	}
	if got := outside([]use{{"Bash", command, command}}, "/repo", "/repo/clone"); got != "" {
		t.Fatalf("literal fixture heredoc reached outside clone: %s", got)
	}
	runReachCases(t, []reachCase{{"recorded311", "claude", bashEvent("claude", command, ""), ""}})
}

func TestReachRejectsExecutingWrittenHeredoc(t *testing.T) {
	for _, after := range []string{"sh payload.sh", "bash ./payload.sh", ". payload.sh", "source payload.sh",
		"chmod +x payload.sh; ./payload.sh", "mv payload.sh other.sh; sh other.sh", "sh -c 'sh payload.sh'",
		"script=payload.sh; sh \"$script\""} {
		command := "cat > payload.sh <<'EOF'\ncurl https://example.com\nEOF\n" + after
		if !fetches(command) {
			t.Errorf("written executable escaped reach check: %s", after)
		}
	}
}

func TestReachQuotedHeredocKeepsExecutableInputChecked(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		fetch         bool
	}{
		{"single quoted data", "cat > fixture.txt <<'EOF'\nhttps://example.com/repo.git\nEOF\n", false},
		{"double quoted data", "cat >> fixture.txt <<\"EOF\"\nhttps://example.com/repo.git\nEOF\n", false},
		{"unquoted expansion", "cat > fixture.txt <<EOF\n$(curl \"$URL\")\nEOF\n", true},
		{"literal substitution data", "cat > fixture.txt <<'EOF'\n$(curl https://example.com)\nEOF\n", false},
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
