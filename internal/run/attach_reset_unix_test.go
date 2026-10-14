//go:build unix

package run

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A background process that closes on an input it never read, after the run's final reply, resets the socket on
// Linux: attach still returns how the run ended.
func TestAttachIgnoresAResetAfterTheFinalReply(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".atm"), 0o755)
	l, e := net.Listen("unix", short(socket(root)))
	if err != nil || e != nil {
		t.Fatal(err, e)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()          // with the input unread
		for b := make([]byte, 1); b[0] != '\n'; { // the request, a byte at a time, and no further
			if _, err := c.Read(b); err != nil {
				return
			}
		}
		raw, err := c.(*net.UnixConn).SyscallConn()
		if err != nil {
			return
		}
		_ = raw.Read(func(fd uintptr) bool { // until the input arrives, left in the socket
			n, _, err := syscall.Recvfrom(int(fd), make([]byte, 1), syscall.MSG_PEEK)
			return n > 0 || err != syscall.EAGAIN
		})
		_ = json.NewEncoder(c).Encode(reply{Run: &Outcome{Run: "1", Outcome: "passed", Ended: time.Now()}})
	}()
	in := make(chan Input, 1)
	in <- Input{Keys: []byte("q")}
	o, err := Follow(root, "1", nil, in, func(Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	if o.Outcome != "passed" {
		t.Fatalf("outcome %q, want passed", o.Outcome)
	}
}
