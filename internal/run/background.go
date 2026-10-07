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
	return Human(cmp.Or(o.Ended, time.Now()).Sub(o.Started))
}

// Err is the ended run as atm's error: nil when it passed, else its reason under the failed node's exit code.
func (o Outcome) Err() error {
	if o.Outcome == "passed" {
		return nil
	}
	return exitError{cmp.Or(codes[o.FailedNode], 2), errors.New(o.Reason)}
}

type request struct {
	Cmd  string   `json:"cmd"` // run, attach or runs
	Args []string `json:"args,omitempty"`
	Run  string   `json:"run,omitempty"`
	Size *[2]int  `json:"size,omitempty"` // the columns and rows of the terminal that attaches, if one does
}

// Frame is a piece of a run's screen.
type Frame struct {
	Line  *string         `json:"line,omitempty"`  // a line of its plain screen
	Event json.RawMessage `json:"event,omitempty"` // a node's start or end, or a live event inside it
	Raw   []byte          `json:"raw,omitempty"`   // the delivery command's output on the attached terminal
	PTY   bool            `json:"pty,omitempty"`   // the delivery command's terminal has started
}

// Input is what an attached terminal sends after its request: keys, and its new size, for the delivery
// command that runs on it.
type Input struct {
	Keys []byte  `json:"keys,omitempty"`
	Size *[2]int `json:"size,omitempty"`
}

type reply struct {
	Frame
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
	_ = os.Remove(sock) // a dead one's; connect, which starts it, made .atm
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
// at each frame of its screen and closed for good at its end. ttys terminals of size, the last one's, are
// attached to it; pty is the terminal its delivery command runs on, while it does.
type bgRun struct {
	Outcome
	frames []Frame
	more   chan struct{}
	ttys   int
	size   [2]int
	pty    *os.File
}

// add puts f on r's screen. Under the server's lock.
func (r *bgRun) add(f Frame) {
	r.frames = append(r.frames, f)
	close(r.more)
	r.more = make(chan struct{})
}

func serve(l *net.UnixListener, root, sock string) error {
	runs, err := history(root)
	if err != nil {
		return err
	}
	s := &server{root: root, runs: runs, last: time.Now()}
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
	dec := json.NewDecoder(c)
	if err := dec.Decode(&req); err != nil {
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
		s.attach(req, dec, enc)
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
	r.Report = filepath.Join(runDir(s.root, r.Run), "report.json")
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
		return Run(r.Run, args, &screen{s: s, r: r, events: true}, &screen{s: s, r: r})
	}()
	s.Lock()
	defer s.Unlock()
	o := &r.Outcome
	o.Outcome, o.Ended, s.last = outcome(err), time.Now(), time.Now()
	if err != nil {
		o.Reason, _, _ = strings.Cut(err.Error(), "\n")
	}
	if o.Step == "" { // no first node, so report.json was never written
		o.Report = ""
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

// attach sends the screen of run req.Run, the last one that runs when it is "", or else the last one, as it
// grows, and then how it ended. A terminal's input, from dec, goes to the run's delivery command.
func (s *server) attach(req request, dec *json.Decoder, enc *json.Encoder) {
	label := req.Run
	r := s.find(label)
	if r == nil {
		msg := "no run yet"
		if label != "" {
			msg = "no run " + label
		}
		_ = enc.Encode(reply{Error: msg})
		return
	}
	if req.Size != nil { // a terminal, until it leaves
		s.Lock()
		r.ttys, r.size = r.ttys+1, *req.Size
		s.Unlock()
		defer func() { s.Lock(); r.ttys--; s.Unlock() }()
		go s.input(r, dec)
	}
	for i := 0; ; {
		s.Lock()
		frames, more, o := r.frames[i:], r.more, r.Outcome
		s.Unlock()
		for _, f := range frames {
			if enc.Encode(reply{Frame: f}) != nil {
				return // the client left; the run goes on
			}
		}
		i += len(frames)
		if !o.Ended.IsZero() {
			_ = enc.Encode(reply{Run: &o})
			return
		}
		<-more
	}
}

// find is run label, the last one that runs when label is "", or else the last one; nil when there is none.
func (s *server) find(label string) *bgRun {
	s.Lock()
	defer s.Unlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].Run == label || label == "" && s.runs[i].Ended.IsZero() {
			return s.runs[i]
		}
	}
	if label == "" && len(s.runs) > 0 {
		return s.runs[len(s.runs)-1]
	}
	return nil
}

// input hands the keys and sizes an attached terminal sends from dec to r's delivery command on a terminal,
// while it runs, until the terminal leaves.
func (s *server) input(r *bgRun, dec *json.Decoder) {
	for {
		var in Input
		if dec.Decode(&in) != nil {
			return
		}
		s.Lock()
		if in.Size != nil {
			r.size = *in.Size
		}
		p := r.pty
		s.Unlock()
		if p != nil && in.Size != nil {
			resize(p, *in.Size)
		}
		if p != nil && len(in.Keys) > 0 {
			_, _ = p.Write(in.Keys) // its command may just have ended
		}
	}
}

// screen is a run's screen: what Run writes, a line at a time, its node events, when events, as a line each
// with the event behind it, and the live events Run has it watch. Output written while the delivery command
// runs on a terminal goes as it is.
type screen struct {
	s      *server
	r      *bgRun
	events bool
	lines  lineWriter
}

func (w *screen) Write(p []byte) (int, error) {
	w.s.Lock()
	raw := !w.events && w.r.pty != nil
	if raw {
		w.r.add(Frame{Raw: bytes.Clone(p)})
	}
	w.s.Unlock()
	if raw {
		return len(p), nil
	}
	if w.lines.fn == nil {
		w.lines.fn = w.line
	}
	return w.lines.Write(p)
}

func (w *screen) line(line string) {
	w.s.Lock()
	defer w.s.Unlock()
	f := Frame{Line: &line}
	if w.events {
		plain := w.r.event(line)
		f = Frame{Line: &plain, Event: json.RawMessage(line)}
	}
	w.r.add(f)
}

func (w *screen) watch(ev []byte) {
	w.s.Lock()
	defer w.s.Unlock()
	w.r.add(Frame{Event: ev})
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
		line += " " + Human(time.Duration(*ev.DurationMS)*time.Millisecond)
	}
	if why, _, _ := strings.Cut(ev.Error, "\n"); why != "" {
		line += ": " + why
	}
	return line
}

// history is the runs in root's .atm/runs.jsonl, how each last stood. One still running there ran in a
// background process that ended before it did.
func history(root string) ([]*bgRun, error) {
	f, err := os.Open(filepath.Join(root, ".atm", "runs.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // no run yet
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var runs []*bgRun
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
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
		r := &bgRun{Outcome: o} // ended: attach never waits on its more
		if i, ok := at[o.Run]; ok {
			runs[i] = r
			continue
		}
		at[o.Run] = len(runs)
		runs = append(runs, r)
	}
	return runs, sc.Err()
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
	err = call(root, request{Cmd: "run", Args: args}, nil, func(r reply) { o = *r.Run })
	return o, err
}

// Attach copies the plain screen of run label, the last one that runs when label is "", to w until it ends,
// and returns how it ended. Leaving leaves the run going.
func Attach(root, label string, w io.Writer) (Outcome, error) {
	return Follow(root, label, nil, nil, func(f Frame) {
		if f.Line != nil {
			_, _ = fmt.Fprintln(w, *f.Line)
		}
		_, _ = w.Write(f.Raw)
	})
}

// Follow hands fn each frame of the screen of run label, as Attach, until it ends, and returns how it ended.
// A terminal of size, its columns and rows, gets the delivery command: the input from in goes to it.
func Follow(root, label string, size *[2]int, in <-chan Input, fn func(Frame)) (Outcome, error) {
	var o Outcome
	err := call(root, request{Cmd: "attach", Run: label, Size: size}, in, func(r reply) {
		if r.Line != nil || r.Event != nil || r.Raw != nil || r.PTY {
			fn(r.Frame)
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
	err := call(root, request{Cmd: "runs"}, nil, func(r reply) { runs = r.Runs })
	return runs, err
}

// call sends req to the background process of the repository at root, started if need be, then what comes
// from in, and hands each reply to fn.
func call(root string, req request, in <-chan Input, fn func(reply)) error {
	c, err := connect(root)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	enc := json.NewEncoder(c)
	if err := enc.Encode(req); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case i, ok := <-in: // never, when in is nil
				if !ok {
					_ = c.Close()
					return
				}
				if enc.Encode(i) != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
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
		if req.Cmd != "attach" {
			return nil // run and runs each have one reply
		}
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
