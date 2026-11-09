//go:build unix

package run

import (
	"bytes"
	"testing"
	"time"
)

func TestFollowDisconnectsWhenItsScreenLeaves(t *testing.T) {
	top := gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", "sleep 3")), "rev-parse", "--show-toplevel")
	background(t, top)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	leave := make(chan struct{})
	first := make(chan struct{}, 1)
	ended := make(chan error, 1)
	go func() {
		_, err := Follow(top, o.Run, leave, func(Frame) {
			select {
			case first <- struct{}{}:
			default:
			}
		})
		ended <- err
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not send its first frame")
	}
	close(leave)
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("detached Follow returned before the run ended without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("Follow kept following after its screen left")
	}
	if _, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
