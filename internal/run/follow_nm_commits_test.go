//go:build unix

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the shipped CLI with disposable repositories and local command-boundary
// responses. The issue's oracle is PR order, full commit identity, and one event
// per prefixed commit, even when the gate can independently see both ci commits.
func TestRunFollowNoMistakesCommitsInPROrder(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("clone_deleted=%t", deleted), func(t *testing.T) {
			parked := recorded(t, "atm_watch_status_parked.txt")
			fixing := strings.NewReplacer(streamFixingCI...).Replace(recorded(t, "atm_watch_status_completed.txt"))
			bin, top, gh, nm := followRepo(t, atmSet(atmYAML, "delivery", "true"))
			writeFixture(t, nm, parked)
			f := runFollow(t, bin, top, false, nil, "--follow")
			next := func(event string, want map[string]any) map[string]any {
				got := f.next(event, want)
				b, _ := json.Marshal(got)
				t.Log(string(b))
				return got
			}
			for _, step := range followSteps {
				next("step", map[string]any{"step": step})
			}
			clone := next("delivery", nil)["clone"].(string)
			next("gate_waiting", map[string]any{"step": "review", "clone": clone})
			if deleted {
				if err := os.RemoveAll(clone); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFixture(t, nm, fixing)
			}
			subjects := []string{
				"no-mistakes(ci): Fix lint",
				"no-mistakes(review): Remove duplicate check",
				"fix: Plain worker commit",
				"no-mistakes(ci): Fix lint",
				"no-mistakes(lint): Format code",
				"no-mistakes(document): Document \"step\"",
				"fix: mention no-mistakes(review): in prose",
				" no-mistakes(ci): Leading space",
			}
			commits := make([]map[string]string, len(subjects))
			for i, subject := range subjects {
				commits[i] = map[string]string{"oid": fmt.Sprintf("%040x", i+1), "messageHeadline": subject}
			}
			publish := func(state string) {
				b, _ := json.Marshal(map[string]any{"url": recordedPR, "state": state, "commits": commits})
				writeFixture(t, gh, string(b))
			}
			publish("OPEN")
			next("pr_opened", map[string]any{"url": recordedPR})
			for _, c := range []struct {
				index int
				step  string
			}{{0, "ci"}, {1, "review"}, {3, "ci"}, {4, "lint"}, {5, "document"}} {
				next("gate_fixing_ci", map[string]any{"commit": map[string]any{
					"sha": commits[c.index]["oid"], "subject": subjects[c.index], "step": c.step,
				}})
			}
			// Allow another real ten-second poll before merging the same PR list.
			// Any repeated or plain commit is queued and fails the next assertion.
			time.Sleep(11 * time.Second)
			publish("MERGED")
			next("merged", map[string]any{"url": recordedPR})
			f.ended(0)
		})
	}
}

func TestFollowStreamNoMistakesCommitsInPROrder(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("clone_deleted=%t", deleted), func(t *testing.T) {
			root := repo(t, "git@github.com:o/r.git", "")
			s := &streamed{t: t, clone: t.TempDir(), status: filepath.Join(t.TempDir(), "status")}
			fakeStatus(t)
			for _, env := range []string{"ATM_STATUS_LIVE", "ATM_STATUS_WAITING", "ATM_STATUS_MERGED"} {
				t.Setenv(env, s.status)
			}
			fakeGH(t, "", false)
			s.gate("completed", streamFixingCI...)
			if deleted {
				if err := os.RemoveAll(s.clone); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			o := &observer{w: &out, now: time.Now, clone: s.clone, branch: "delivery", delivering: true,
				ci: map[string]bool{}}
			s.pr("OPEN")
			o.poll(context.Background(), root)
			o.poll(context.Background(), root) // consume the status fake's failed second read

			subjects := []string{
				"no-mistakes(ci): Fix lint",
				"no-mistakes(review): Remove duplicate check",
				"fix: Plain worker commit",
				"no-mistakes(ci): Repair CI",
			}
			s.pr("OPEN", subjects...)
			o.poll(context.Background(), root)
			o.poll(context.Background(), root) // the same commits must not appear again
			s.pr("MERGED", subjects...)
			if code, done := o.poll(context.Background(), root); code != 0 || !done {
				t.Fatalf("merged poll = %d, %t", code, done)
			}
			var got, want []any
			for _, event := range events(t, &out) {
				if event["event"] == "gate_fixing_ci" {
					got = append(got, event["commit"])
				}
			}
			for _, c := range []struct {
				index int
				step  string
			}{{0, "ci"}, {1, "review"}, {3, "ci"}} {
				want = append(want, map[string]any{
					"sha": fmt.Sprintf("%040d", c.index+1), "subject": subjects[c.index], "step": c.step,
				})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("gate_fixing_ci commits = %v, want %v", got, want)
			}
		})
	}
}
