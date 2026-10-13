package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// helloBrief is all atm doctor --agent asks the agent.
const helloBrief = "Reply with the single word OK and nothing else."

// Hello says hello to the agent c names the way a run does, with the run's command line and reader, in a
// temporary directory it removes, with pipes and no terminal, and returns how long it took to answer OK. Its
// error is the cause, then the agent's last 5 lines.
func Hello(c config.Agent, limit time.Duration) (time.Duration, error) {
	dir, err := os.MkdirTemp("", "atm-hello-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	skill, err := placeSkill(dir, c.Harness, "ponytail")
	if err != nil {
		return 0, err
	}
	final := filepath.Join(dir, "final.md")
	argv, stdin := agent{Agent: c}.argv(dir, skill, final, helloBrief)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = dir, strings.NewReader(stdin), time.Second
	cmd.Env = agentEnv(nil)
	var log bytes.Buffer
	s := stream{live: func(map[string]any) {}}
	start := time.Now()
	err = s.read(cmd, &log, c.Harness)
	took := time.Since(start)
	if c.Harness == "codex" {
		b, _ := os.ReadFile(final)
		s.final = string(b)
	}
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("timed out after %s", Human(limit))
	case ctx.Err() != nil:
		err = errors.New("interrupted")
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		err = fmt.Errorf("exit code %d", exit.ExitCode())
	case errors.As(err, &exit):
		err = errors.New(exit.String()) // signal: killed
	case err != nil:
	case log.Len() == 0:
		err = errors.New("silent")
	case !strings.Contains(s.final, "OK"):
		err = errors.New("no OK in its final message")
	default:
		return took, nil
	}
	return took, fmt.Errorf("%w: %s", err, tail(log.String(), 5))
}

// tail is the last n lines of the agent's log, each as it wrote it, in one line.
func tail(log string, n int) string {
	ls := strings.Split(strings.TrimSpace(log), "\n")
	ls = ls[max(0, len(ls)-n):]
	for i, l := range ls {
		_ = json.Unmarshal([]byte(l), &ls[i]) // a line that was not JSON is a JSON string
	}
	return strings.Join(ls, " | ")
}
