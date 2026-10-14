package run

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// findingRow and commitRow are what the follow stream reads of a finding and of a CI repair commit.
type findingRow struct{ id, severity, file, description string }

type commitRow struct{ sha, subject string }

const recordedPR = "https://github.com/thellmwhisperer/agentic-team-member/pull/315"

// readGate is the gate a gateState reads of a clone whose `no-mistakes axi status` is status, or fails when
// statusFails: the state it read and its error.
func readGate(t *testing.T, status string, statusFails bool) (reflect.Value, error) {
	t.Helper()
	count := fakeStatus(t)
	if statusFails {
		writeFixture(t, count, "1") // the fake's second call fails
	}
	live := filepath.Join(t.TempDir(), "status.txt")
	writeFixture(t, live, status)
	t.Setenv("ATM_STATUS_LIVE", live)
	var s gateState
	read := reflect.ValueOf(&s).MethodByName("Read")
	if !read.IsValid() {
		t.Fatal("gateState has no Read: the follow stream cannot read its gate")
	}
	err, _ := read.Call([]reflect.Value{reflect.ValueOf(t.TempDir())})[0].Interface().(error)
	return reflect.ValueOf(s), err
}

func recorded(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFollowGateDataKeepsEveryFieldOfTheWaitingFindings(t *testing.T) {
	s, err := readGate(t, recorded(t, "atm_watch_status_parked.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.FieldByName("waiting").String(); got != "review" {
		t.Fatalf("waiting = %q, want review", got)
	}
	want := []findingRow{
		{"doctor-duplicate-watcher", "error", "internal/cli/doctor.go", "`atm doctor` now prints and counts the " +
			"watcher check twice: the existing block is at lines 68–75 and the added copy is at lines 78–85. This " +
			"breaks the existing check-count contract and tests. Remove the added copy."},
		{"follow-ci-commit-provenance-unresolved", "error", "internal/run/follow.go", "Criterion 2 requires the SHA " +
			"and subject of each commit added while fixing CI. The latest fix round's `s.fixing` guard omits a CI " +
			"commit when fixing completes between polls, while a review commit observed during CI fixing can still " +
			"be labelled as a CI commit. The baseline is also learned from the delivery-passed frame at " +
			"`internal/run/follow.go:180`, after the gate may have advanced; `internal/run/run.go:275` records that " +
			"initial SHA earlier, and `internal/run/follow_test.go:156` delays the fake gate until delivery ends. " +
			"Correct attribution needs authoritative commit-to-step provenance, so the remedy extends the available " +
			"status contract and needs a decision."},
	}
	if got := fmt.Sprintf("%q", s.FieldByName("findings")); got != fmt.Sprintf("%q", want) {
		t.Fatalf("findings = %s\nwant %s", got, fmt.Sprintf("%q", want))
	}
}

func TestFollowGateDataHasNoWaitingPayloadWithoutAWaitingStep(t *testing.T) {
	parked := recorded(t, "atm_watch_status_parked.txt")
	s, err := readGate(t, strings.Replace(parked, "awaiting_agent: parked", "awaiting_agent: none", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.FieldByName("findings").IsValid() {
		t.Fatal("gateState has no findings")
	}
	if w, f := s.FieldByName("waiting").String(), s.FieldByName("findings").Len(); w != "" || f != 0 {
		t.Fatalf("waiting = %q with %d findings, want none", w, f)
	}
}

// Whatever CI does when the gate is read, fixing or green, its PR's CI repair commits are the same: those whose
// subject starts `no-mistakes(ci):`, in order, never the worker's or a review's.
func TestFollowGateDataReadsEachCIRepairCommitWhetherFixingOrGreen(t *testing.T) {
	green := recorded(t, "atm_watch_status_completed.txt")
	fixing := strings.Replace(green, "ci,completed,", "ci,fixing,", 1)
	want := fmt.Sprintf("%q", []commitRow{
		{"9df398a19fdc2b1ac4966b0d5536d06d557f00c0", `no-mistakes(ci): Fix lint, "unused" import`},
		{"b946fbc038d6e1bddb9e4735e04a86828fb64441", "no-mistakes(ci): Retry the lease read on Windows"},
	})
	for name, status := range map[string]string{"green": green, "fixing": fixing} {
		t.Run(name, func(t *testing.T) {
			args := fakeGH(t, recorded(t, "gh_pr_view_commits.json"), false)
			s, err := readGate(t, status, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%q", s.FieldByName("ci")); got != want {
				t.Fatalf("ci commits = %s, want %s", got, want)
			}
			if b, err := os.ReadFile(args); err != nil || string(b) != "pr view "+recordedPR+" --json commits" {
				t.Fatalf("gh args = %q, %v", b, err)
			}
		})
	}
}

func TestFollowGateDataFailsOnAFailedCommandWithoutInventingEvents(t *testing.T) {
	completed := recorded(t, "atm_watch_status_completed.txt")
	for name, c := range map[string]struct {
		status               string
		statusFails, ghFails bool
	}{"status": {completed, true, false}, "no gate": {"", false, false}, "gh": {completed, false, true}} {
		t.Run(name, func(t *testing.T) {
			fakeGH(t, recorded(t, "gh_pr_view_commits.json"), c.ghFails)
			s, err := readGate(t, c.status, c.statusFails)
			if err == nil {
				t.Fatal("read succeeded, want its command's error")
			}
			if !s.FieldByName("ci").IsValid() {
				t.Fatal("gateState has no ci")
			}
			if s.FieldByName("ci").Len() != 0 || s.FieldByName("seen").Bool() || s.FieldByName("green").Bool() {
				t.Fatalf("failed read left %+v, want nothing", s)
			}
		})
	}
}
