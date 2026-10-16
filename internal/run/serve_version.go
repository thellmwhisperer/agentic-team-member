package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/buildinfo"
)

// matchingServe checks the connected process before sending it any run. Old
// serves already log their identity, so they need no protocol upgrade.
func matchingServe(root string, c net.Conn) (again bool, err error) {
	pid, pidErr := servePID(c)
	if pidErr == nil && pid == os.Getpid() {
		return false, nil // an in-process serve necessarily runs this build
	}
	version := loggedServeVersion(root, pid)
	if pidErr == nil && version == buildinfo.String() {
		return false, nil
	}
	// An idle serve whose executable was replaced closes accepted connections
	// while handing off. Probe without submitting work: EOF means retry safely.
	// A reply also establishes that startup has finished writing its identity.
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return false, err
	}
	probeErr := json.NewEncoder(c).Encode(request{Cmd: "runs"})
	if probeErr == nil {
		var r reply
		probeErr = json.NewDecoder(c).Decode(&r)
	}
	if errors.Is(probeErr, io.EOF) || errors.Is(probeErr, syscall.ECONNRESET) || errors.Is(probeErr, syscall.EPIPE) {
		return true, nil
	}
	version = loggedServeVersion(root, pid)
	if pidErr == nil && probeErr == nil && version == buildinfo.String() {
		return true, nil // the probe consumed this connection; reconnect for the run
	}
	return false, fmt.Errorf("serve %s is older than atm %s", version, buildinfo.String())
}

// Only the last identity for this connection's pid counts. In particular a
// newer process's log entry cannot authorize a connection to an older serve.
func loggedServeVersion(root string, pid int) string {
	b, err := os.ReadFile(filepath.Join(root, ".atm", "serve.log"))
	if err != nil || pid <= 0 {
		return "unknown"
	}
	current, version := 0, "unknown"
	for _, line := range strings.Split(string(b), "\n") {
		if value, ok := strings.CutPrefix(line, "atm serve pid: "); ok {
			current, _ = strconv.Atoi(value)
			if current == pid {
				version = "unknown"
			}
		} else if value, ok := strings.CutPrefix(line, "atm serve version: "); ok && current == pid && value != "" {
			version = value
		}
	}
	return version
}

func servePID(c net.Conn) (int, error) {
	u, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("serve connection is not a Unix socket")
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var peerErr error
	err = raw.Control(func(fd uintptr) { pid, peerErr = socketPeerPID(fd) })
	return pid, errors.Join(err, peerErr)
}
