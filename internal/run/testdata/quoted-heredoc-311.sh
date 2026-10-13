cat >> internal/run/followup_test.go <<'EOF'

func TestRunKeepsTheFollowUpsOfAUnitThatFailsChecks(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git",
		atmSet(atmYAML, "test", `if [ -f c1_test.sh ]; then false; else sh a_test.sh; fi`))
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; mkdir -p .atm/follow-ups/1; `+
		`echo 'test -f ios.txt' > .atm/follow-ups/1/ios_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"ios","red_test":"ios_test.sh",`+
		`"criterion":"iOS retries too."}]}' > .atm/fake-report.json; `+
		`else printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; `+
		`mkdir -p .atm/follow-ups/1 .atm/follow-ups/2; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; `+
		`echo 'test -f android.txt' > .atm/follow-ups/2/android_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[`+
		`{"title":"chained","red_test":"c1_test.sh","criterion":"A retry runs once after a timeout."},`+
		`{"title":"android","red_test":"android_test.sh","criterion":"Android retries too."}]}' `+
		`> .atm/fake-report.json; fi`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want unit 2's checks to fail")
	}
	got := readFollowUps(t, root)
	if len(got) != 2 || got[0]["title"] != "android" || got[0]["verified"] != true ||
		got[1]["title"] != "ios" || got[1]["verified"] != false || got[1]["test"] != "test -f ios.txt\n" {
		t.Fatalf("want unit 1's follow-up, then unit 2's not verified, got %v", got)
	}
}

func TestRunNotesInvalidFollowUpsNextToAChecksFailure(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "test", "false"))
	t.Setenv("FAKE_AGENT_WORK", fixWork+`; printf '{"test_file":"a_test.sh","follow_ups":"nope"}' > .atm/fake-report.json`)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard)
	if err == nil || strings.Contains(err.Error(), "follow_ups") {
		t.Fatalf("want the checks failure as the reason, got %v", err)
	}
	checks := ends(t, &out, "checks")
	if note, _ := json.Marshal(checks[0]["follow_ups"]); !strings.Contains(string(note), "invalid follow_ups") {
		t.Fatalf("want the invalid follow_ups noted on the checks event, got %s", note)
	}
	if got := readFollowUps(t, root); len(got) != 0 {
		t.Fatalf("want no follow-ups, got %v", got)
	}
}
EOF
go test ./internal/run -run 'TestRunKeepsTheFollowUpsOfAUnitThatFailsChecks|TestRunNotesInvalidFollowUps' -count=1 2>&1 | tail -20