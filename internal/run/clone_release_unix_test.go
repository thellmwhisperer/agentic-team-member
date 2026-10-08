//go:build unix

package run

import (
	"bytes"
	"path/filepath"
	"testing"
)

// A run that ends at its delivery, passed or failed, lets go of its kept clone: the process its delivery left
// keeps its hold, ATM's is gone. Once that process is gone too and the branch is merged, the next run of the same
// background process sweeps the clone, its .lock and its .delivered.
func TestRunEndLetsGoOfItsKeptClone(t *testing.T) {
	for _, c := range []struct{ name, exit string }{{"passed", "0"}, {"failed", "1"}} {
		t.Run(c.name, func(t *testing.T) {
			bare := t.TempDir()
			gitT(t, bare, "init", "-q", "--bare")
			fifos(t) // the delivery's process waits on ATM_TEST_GATE until killed
			out := t.TempDir()
			t.Setenv("ATM_TEST_OUT", out)
			root := repo(t, bare, atmSet(atmYAML, "delivery",
				`cat "$ATM_TEST_GATE" >/dev/null 2>&1 & echo $! > "$ATM_TEST_OUT/pid"; exit `+c.exit))
			gitT(t, root, "config", "user.name", "Repo Dev")
			gitT(t, root, "config", "user.email", "dev@example.com")
			top := gitT(t, root, "rev-parse", "--show-toplevel")
			background(t, top)
			o := startBackgroundRun(t, top)
			if end, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil || end.Outcome != c.name ||
				end.Step != "delivery" {
				t.Fatalf("Attach = %+v, %v", end, err)
			}
			clone := theClone(t, top)
			holder := recordedProcess(t, filepath.Join(out, "pid"))
			if f, locked, err := tryCloneExclusive(clone); err != nil || locked {
				if f != nil {
					_ = f.Close()
				}
				t.Fatalf("the delivery's process lost its hold on the clone: %v, %v", locked, err)
			}
			if err := holder.Kill(); err != nil {
				t.Fatal(err)
			}
			until(t, "the clone's lock free once the delivery's process is gone", func() bool {
				f, locked, err := tryCloneExclusive(clone)
				if f != nil {
					_ = f.Close()
				}
				return err == nil && locked
			})
			t.Setenv("FAKE_AGENT", "fail")
			assertCloneKept(t, top, clone, "the sweep removed a clone whose branch is not merged")
			gitT(t, clone, "push", "-q", "origin", "HEAD:main")
			nextRun(t, top)
			if left := clones(t, top); len(left) != 0 {
				t.Fatalf("the merged clone outlived the sweep: %v", left)
			}
		})
	}
}
