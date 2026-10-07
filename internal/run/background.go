package run

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The background process of a repository runs its runs, each in itself, so closing the terminal that started
// one leaves it going. The first atm command that needs it starts it, as atm serve, and it listens on
// .atm/atm.sock until it has had no run for idle. A client sends it one request, a JSON line, and reads JSON
// lines back until it closes the connection. How each run ended goes to .atm/runs.jsonl.
// ponytail: a Unix socket on Windows too, which Windows 10 and later have; a named pipe is the upgrade.

// Outcome is a run of the background process: running, or how it ended.
type Outcome struct {
	Outcome    string    `json:"outcome"` // running, passed or failed
	Run        string    `json:"run"`     // its label: the issue's name and the run's number in the repository
	FailedNode string    `json:"failed_node"`
	Reason     string    `json:"reason"`
	Report     string    `json:"report"` // report.json, none when the run failed before its first node
	NextStep   string    `json:"next_step"`
	Issue      string    `json:"issue"`
	Step       string    `json:"step"` // the last node it started
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended"`
}

// Duration is how long the run took, or has taken so far, as ATM prints every time.
func (o Outcome) Duration() string {
	return human(cmp.Or(o.Ended, time.Now()).Sub(o.Started))
}

// Err is the ended run as atm's error: nil when it passed, else its reason under the failed node's exit code.
func (o Outcome) Err() error {
	if o.Outcome == "passed" {
		return nil
	}
	return exitError{cmp.Or(codes[o.FailedNode], 2), errors.New(cmp.Or(o.Reason, "the run "+o.Outcome))}
}

type request struct {
	Cmd  string   `json:"cmd"` // run, attach or runs
	Args []string `json:"args,omitempty"`
	Run  string   `json:"run,omitempty"`
}

type reply struct {
	Line  *string   `json:"line,omitempty"` // a line of the run's screen
	Run   *Outcome  `json:"run,omitempty"`
	Runs  []Outcome `json:"runs,omitempty"`
	Error string    `json:"error,omitempty"`
}

// idle is how long the background process waits without a run before it ends; tick is how often it checks.
var idle, tick = 10 * time.Minute, 5 * time.Second

// live holds the clones of the runs the background process runs, nil outside it.
var live struct {
	sync.Mutex
	dirs map[string]bool
}

// hold marks the run of clone dir alive in the background process, or, when on is false, gone.
func hold(dir string, on bool) {
	live.Lock()
	defer live.Unlock()
	if !on {
		delete(live.dirs, dir)
	} else if live.dirs != nil {
		live.dirs[dir] = true
	}
}

// running says whether the run that wrote pid next to clone dir still runs. In the background process, its
// runs all have its pid and it holds those alive: one with its pid it does not hold died, or ran in the process
// before it whose pid it got.
func running(dir string, pid int) bool {
	live.Lock()
	defer live.Unlock()
	if live.dirs != nil && pid == os.Getpid() {
		return live.dirs[dir]
	}
	return alive(pid)
}

// socket is the background process's socket for the repository at root.
func socket(root string) string { return filepath.Join(root, ".atm", "atm.sock") }

// short is path relative to the working directory when that is shorter: a socket's path has to fit in about
// a hundred bytes.
func short(path string) string {
	wd, err := os.Getwd()
	if err == nil {
		wd, err = filepath.EvalSymlinks(wd)
	}
	if rel, e := filepath.Rel(wd, path); err == nil && e == nil && len(rel) < len(path) {
		return rel
	}
	return path
}

// Serve is the background process of the repository at root, its working directory. It ends when it has had
// no run for idle, when its socket is removed while no run goes, or at once when another one serves root.
func Serve(root string) error {
	sock := socket(root)
	if c, err := net.Dial("unix", short(sock)); err == nil {
		_ = c.Close()
		return errors.New("a background process already serves " + root)
	}
	// ponytail: two started at once may both get here and the second one's socket wins; a lock file is the upgrade.
	_ = os.Remove(sock) // a dead one's
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	l, err := net.Listen("unix", short(sock))
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	if err := os.Chmod(sock, 0o600); err != nil { // whoever reaches it runs agents as this user
		return err
	}
	return serve(l.(*net.UnixListener), root, sock)
}

// server is the background process's state, under its lock.
type server struct {
	sync.Mutex
	root string
	runs []*bgRun
	last time.Time // when it last had a run going
}

// bgRun is a run of the background process: its outcome, its screen so far, and more, closed and replaced
// at each line of its screen and closed for good at its end.
type bgRun struct {
	Outcome
	lines []string
	more  chan struct{}
}

func serve(l *net.UnixListener, root, sock string) error {
	s := &server{root: root, runs: history(root), last: time.Now()}
	live.Lock()
	live.dirs = map[string]bool{}
	live.Unlock()
	defer func() { live.Lock(); live.dirs = nil; live.Unlock() }()
	for {
		_ = l.SetDeadline(time.Now().Add(tick))
		c, err := l.Accept()
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			if err != nil {
				return err
			}
			go s.handle(c)
			continue
		}
		_, gone := os.Stat(sock)
		if s.Lock(); s.busy() == 0 && (gone != nil || time.Since(s.last) > idle) {
			s.Unlock()
			return nil
		}
		s.Unlock()
	}
}

// busy is how many runs go.
func (s *server) busy() (n int) {
	for _, r := range s.runs {
		if r.Ended.IsZero() {
			n++
		}
	}
	return n
}

func (s *server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	var req request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	enc := json.NewEncoder(c)
	switch req.Cmd {
	case "run":
		o := s.start(req.Args)
		_ = enc.Encode(reply{Run: &o})
	case "runs":
		s.Lock()
		runs := make([]Outcome, len(s.runs))
		for i, r := range s.runs {
			runs[i] = r.Outcome
		}
		s.Unlock()
		_ = enc.Encode(reply{Runs: runs})
	case "attach":
		s.attach(req.Run, enc)
	default:
		_ = enc.Encode(reply{Error: "unknown request " + strconv.Quote(req.Cmd)})
	}
}

// start runs args, atm run's command line, in the background and returns the run, just started.
func (s *server) start(args []string) Outcome {
	issue := ""
	if len(args) > 0 {
		issue = args[len(args)-1]
	}
	name := nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSuffix(filepath.Base(issue),
		filepath.Ext(issue))), "-")
	s.Lock()
	defer s.Unlock()
	r := &bgRun{more: make(chan struct{}), Outcome: Outcome{Outcome: "running", Issue: issue, Started: time.Now(),
		Run: cmp.Or(strings.Trim(name, "-"), "run") + "-" + strconv.Itoa(len(s.runs)+1)}}
	s.runs = append(s.runs, r)
	s.save(r.Outcome)
	go s.run(r, args)
	return r.Outcome
}

func (s *server) run(r *bgRun, args []string) {
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil { // one run's crash ends that run, not every run
				err = fmt.Errorf("atm crashed: %v", p)
			}
		}()
		return Run(args, &screen{s: s, r: r, events: true}, &screen{s: s, r: r})
	}()
	s.Lock()
	defer s.Unlock()
	o := &r.Outcome
	o.Outcome, o.Ended, s.last = outcome(err), time.Now(), time.Now()
	if err != nil {
		o.Reason, _, _ = strings.Cut(err.Error(), "\n")
	}
	if o.Step != "" { // its first node writes report.json
		o.Report = filepath.Join(s.root, ".atm", "report.json")
	}
	again := ", then run it again: atm run " + o.Issue
	switch {
	case o.Outcome == "passed" && o.Step == "delivery":
		o.NextStep = "review the branch the delivery command got"
	case o.Outcome == "passed":
		o.NextStep = "nothing kept the work: set delivery in .atm.yaml to hand it on"
	case o.FailedNode == "":
		o.NextStep = "fix the reason" + again
	default:
		o.NextStep = "read why " + o.FailedNode + " failed in the report, fix it" + again
	}
	s.save(*o)
	close(r.more)
}

// attach sends the screen of run label, the last one that runs when label is "", or else the last one, as it
// grows, and then how it ended.
func (s *server) attach(label string, enc *json.Encoder) {
	s.Lock()
	var r *bgRun
	for i := len(s.runs) - 1; i >= 0 && r == nil; i-- {
		if s.runs[i].Run == label || label == "" && s.runs[i].Ended.IsZero() {
			r = s.runs[i]
		}
	}
	if r == nil && label == "" && len(s.runs) > 0 {
		r = s.runs[len(s.runs)-1]
	}
	s.Unlock()
	if r == nil {
		msg := "no run yet"
		if label != "" {
			msg = "no run " + label
		}
		_ = enc.Encode(reply{Error: msg})
		return
	}
	for i := 0; ; {
		s.Lock()
		lines, more, o := r.lines[i:], r.more, r.Outcome
		s.Unlock()
		for _, l := range lines {
			if enc.Encode(reply{Line: &l}) != nil {
				return // the client left; the run goes on
			}
		}
		i += len(lines)
		if !o.Ended.IsZero() {
			_ = enc.Encode(reply{Run: &o})
			return
		}
		<-more
	}
}

// screen is a run's screen: what Run writes, a line at a time, its node events, when events, as a line each.
type screen struct {
	s      *server
	r      *bgRun
	events bool
	buf    []byte
}

func (w *screen) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		w.s.Lock()
		if w.events {
			line = w.r.event(line)
		}
		w.r.lines = append(w.r.lines, line)
		close(w.r.more)
		w.r.more = make(chan struct{})
		w.s.Unlock()
	}
}

// event is the screen line of node event line, which it notes in the run's outcome.
func (r *bgRun) event(line string) string {
	var ev struct {
		Step, State, Error string
		DurationMS         *int64 `json:"duration_ms"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return line
	}
	r.Step = ev.Step
	if ev.State == "failed" {
		r.FailedNode = ev.Step
	}
	line = fmt.Sprintf("%-9s %s", ev.Step, ev.State)
	if ev.DurationMS != nil {
		line += " " + human(time.Duration(*ev.DurationMS)*time.Millisecond)
	}
	if why, _, _ := strings.Cut(ev.Error, "\n"); why != "" {
		line += ": " + why
	}
	return line
}

// history is the runs in root's .atm/runs.jsonl, how each last stood. One still running there ran in a
// background process that ended before it did.
func history(root string) []*bgRun {
	f, err := os.Open(filepath.Join(root, ".atm", "runs.jsonl"))
	if err != nil {
		return nil // no run yet
	}
	defer func() { _ = f.Close() }()
	var runs []*bgRun
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var o Outcome
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		if o.Ended.IsZero() {
			o.Outcome, o.FailedNode, o.Ended = "failed", o.Step, o.Started
			o.Reason = "the background process ended before the run did"
			o.NextStep = "run it again: atm run " + o.Issue
		}
		r := &bgRun{Outcome: o, more: make(chan struct{})}
		close(r.more)
		if i, ok := at[o.Run]; ok {
			runs[i] = r
			continue
		}
		at[o.Run] = len(runs)
		runs = append(runs, r)
	}
	return runs
}

// save appends o to runs.jsonl. ponytail: the file only grows; trimming it is the upgrade.
func (s *server) save(o Outcome) {
	b, _ := json.Marshal(o)
	f, err := os.OpenFile(filepath.Join(s.root, ".atm", "runs.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "atm serve:", err) // its log; the run goes on
	}
}

// Start checks args, atm run's command line, and has the background process of the repository at root run it.
// It returns the run, just started.
func Start(root string, args []string) (Outcome, error) {
	fs, _, _, err := parseArgs(args)
	if err != nil {
		return Outcome{}, err
	}
	args = append([]string(nil), args...)
	if _, err := strconv.Atoi(fs.Arg(0)); err != nil { // a file, which the background process reads from root
		if args[len(args)-1], err = filepath.Abs(fs.Arg(0)); err != nil {
			return Outcome{}, err
		}
	}
	var o Outcome
	err = call(root, request{Cmd: "run", Args: args}, func(r reply) { o = *r.Run })
	return o, err
}

// Attach copies the screen of run label, the last one that runs when label is "", to w until it ends, and
// returns how it ended. Leaving leaves the run going.
func Attach(root, label string, w io.Writer) (Outcome, error) {
	var o Outcome
	err := call(root, request{Cmd: "attach", Run: label}, func(r reply) {
		if r.Line != nil {
			_, _ = fmt.Fprintln(w, *r.Line)
		}
		if r.Run != nil {
			o = *r.Run
		}
	})
	if err == nil && o.Ended.IsZero() {
		err = errors.New("the background process ended before the run did")
	}
	return o, err
}

// Runs is every run the background process of the repository at root knows, oldest first.
func Runs(root string) ([]Outcome, error) {
	var runs []Outcome
	err := call(root, request{Cmd: "runs"}, func(r reply) { runs = r.Runs })
	return runs, err
}

// call sends req to the background process of the repository at root, started if need be, and hands each
// reply to fn.
func call(root string, req request, fn func(reply)) error {
	c, err := connect(root)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	dec := json.NewDecoder(c)
	for {
		var r reply
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if r.Error != "" {
			return errors.New(r.Error)
		}
		fn(r)
	}
}

// connect dials the background process of the repository at root, starting it when none answers. It logs to
// .atm/serve.log.
func connect(root string) (net.Conn, error) {
	sock := socket(root)
	if c, err := net.Dial("unix", short(sock)); err == nil {
		return c, nil
	}
	exe, err := os.Executable()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(sock), 0o755)
	}
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(root, ".atm", "serve.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command(exe, "serve")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("unix", short(sock)); err == nil {
			return c, nil
		}
		select {
		case <-exited: // it lost to another one, which answers, or it failed
			if c, err := net.Dial("unix", short(sock)); err == nil {
				return c, nil
			}
			return nil, fmt.Errorf("the background process ended: see %s", log.Name())
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the background process does not answer: see %s", log.Name())
		}
	}
}
