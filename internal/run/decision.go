package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// Decision keeps the finding and the launcher's answer in report.json. An empty answer is pending.
type Decision struct {
	Finding string `json:"finding"`
	Answer  string `json:"answer"`
}

type decisionAnswer struct {
	answer string
	saved  chan error
}

// ask parks the run with its stack and clone intact. Attached clients receive the question and may leave.
func (w *screen) ask(r *verdict) string {
	ch := make(chan decisionAnswer)
	w.s.Lock()
	w.r.answer = ch
	w.s.Unlock()
	r.Decisions = append(r.Decisions, Decision{Finding: r.Reason})
	d := &r.Decisions[len(r.Decisions)-1]
	r.emit("decision", map[string]any{"state": "waiting", "run": w.r.Run,
		"finding": d.Finding, "choices": "approve, fix, stop",
		"message": map[string]string{"checks": "Check", "agent": "Agent"}[r.FailedNode] + " failed: " + d.Finding +
			"\nAnswer: atm answer " + w.r.Run + " approve|fix|stop"})
	reply := <-ch
	d.Answer = reply.answer
	err := r.write()
	answer := "stop"
	if err == nil {
		answer = d.Answer
		r.emit("decision", map[string]any{"state": "answered", "answer": d.Answer, "finding": d.Finding,
			"message": "Answer: " + d.Answer})
	}
	reply.saved <- err
	return answer
}

// decision updates the background outcome before the question frame wakes an attached client.
func (r *bgRun) decision(line string) string {
	var ev struct{ State, Finding, Message, Answer string }
	_ = json.Unmarshal([]byte(line), &ev)
	if ev.State == "waiting" {
		r.Outcome.Outcome, r.Ended = "failed", time.Now()
		r.Reason = ev.Finding
		r.NextStep = "atm answer " + r.Run + " approve|fix|stop; recovery: atm run --resume " + r.Run
	} else {
		r.Outcome.Outcome, r.Ended = "running", time.Time{}
		if ev.Answer != "stop" {
			r.FailedNode, r.Reason = "", ""
		}
		r.NextStep = ""
	}
	return ev.Message
}

func (s *server) answerRun(label, answer string) error {
	if answer != "approve" && answer != "fix" && answer != "stop" {
		return errors.New("answer must be approve, fix or stop")
	}
	r := s.find(label)
	s.Lock()
	if r == nil || r.answer == nil {
		s.Unlock()
		return fmt.Errorf("run %s is not waiting for an answer", label)
	}
	ch := r.answer
	r.answer = nil        // only one caller can consume this question
	r.Ended = time.Time{} // the continuation is active before another request can prune history
	s.Unlock()
	reply := decisionAnswer{answer: answer, saved: make(chan error)}
	ch <- reply
	return <-reply.saved
}

// Answer gives a waiting run its launcher's decision and waits until the answer is on disk.
func Answer(root, label, answer string) error {
	return call(root, request{Cmd: "answer", Run: label, Answer: answer}, nil, func(reply) {})
}

// afterCheck gives the worker exactly one turn with the finding, using its session when supported.
func (a agent) afterCheck(clone, text, session, finding string, attempt int) (map[string]any, error) {
	name := "worker-fix-" + strconv.Itoa(attempt)
	path := filepath.Join(a.dir, name+".jsonl")
	final := filepath.Join(a.dir, name+".final.md")
	ev := map[string]any{"log": path}
	skill, err := placeSkill(clone, a.Harness, "ponytail")
	if err == nil {
		_, err = a.reviewScratch("worker")
	}
	if err == nil {
		prompt := text + "\n\nThe launcher chose fix. Fix this failure in one turn:\n" + finding
		argv, stdin, ok := a.resumeArgv(clone, skill, final, session, prompt)
		if session == "" || !ok {
			argv, stdin = a.argv(clone, skill, final, prompt)
		}
		var stream *stream
		var exit, cause string
		stream, exit, cause, err = a.exec(clone, path, argv, stdin, false)
		var invalid error
		invalid, err = a.reported(name, path, final, exit, cause, err, stream, testFile, ev)
		if invalid != nil {
			ev["report"], err = "missing", nil
			delete(ev, "exit")
			delete(ev, "tail")
		}
	}
	return ev, err
}

func (a agent) checkDecision(r *verdict) string {
	q, ok := r.out.(*screen)
	if !ok || r.FailedNode != "checks" && !errors.As(r.err, new(askError)) {
		return ""
	}
	r.resumable(a.dir)
	answer := q.ask(r)
	if answer != "stop" {
		r.err, r.FailedNode, r.Reason = nil, "", ""
		r.Resumable, r.ResumableUnit = false, 0
	}
	return answer
}

func (a agent) fixCheck(r *verdict, dir, text string, keep *resumePoint) (report map[string]any) {
	a.Agent, a.args, a.env = keep.Agent, keep.Args, keep.Env
	r.step("agent", func() (map[string]any, error) {
		ev, err := a.afterCheck(dir, text, keep.Session, r.Decisions[len(r.Decisions)-1].Finding, len(r.Decisions))
		report, _ = ev["report"].(map[string]any)
		keep.worker(a, ev)
		return ev, err
	})
	return report
}
