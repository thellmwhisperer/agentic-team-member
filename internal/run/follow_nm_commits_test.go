//go:build unix

package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

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
