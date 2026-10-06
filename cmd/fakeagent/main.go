// fakeagent stands in for the coding-agent CLIs ATM drives (claude, codex, opencode, pi) and for gh in
// ATM's e2e tests. Modelled on no-mistakes' cmd/fakeagent (MIT). One binary, linked under each name:
// argv[0] picks the wire protocol, $FAKEAGENT_SCENARIO picks what the agent does to the target repo in
// its working directory (default "fixing"), and every invocation is appended to $FAKEAGENT_LOG as one
// JSON line. The scenarios are the Python fakes of tests/test_harness_worker.py, against the same
// target: calc.py whose add returns a - b, and one test.
package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type obj = map[string]any

// finals turn the agent's last message into the event its real CLI ends with; ATM reads the report from it.
var finals = map[string]func(text string) obj{
	"claude": func(t string) obj {
		return obj{"type": "result", "subtype": "success", "num_turns": 1, "is_error": false, "result": t}
	},
	"codex": func(t string) obj {
		return obj{"type": "item.completed", "item": obj{"type": "agent_message", "text": t}}
	},
	"opencode": func(t string) obj { return obj{"type": "text", "part": obj{"text": t}} },
	"pi": func(t string) obj {
		return obj{"type": "message_end", "message": obj{"role": "assistant",
			"content": []obj{{"type": "text", "text": t}}}}
	},
}

func main() {
	if os.Getenv("FAKEAGENT_GRANDCHILD") != "" {
		fmt.Println(`{"type": "system", "subtype": "grandchild"}`)
		time.Sleep(time.Minute)
		return
	}
	os.Exit(run(os.Args, os.Stdin, os.Stdout))
}

func run(argv []string, stdin io.Reader, stdout io.Writer) int {
	name, args := strings.TrimSuffix(filepath.Base(argv[0]), ".exe"), argv[1:]
	brief := ""
	if name == "claude" {
		b, _ := io.ReadAll(stdin)
		brief = string(b)
	} else if len(args) > 0 {
		brief = args[len(args)-1]
	}
	if err := record(name, args, brief); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: log:", err)
		return 1
	}
	final, ok := finals[name]
	if !ok {
		// gh lands here too: it fails closed, so no test reaches github.com.
		fmt.Fprintf(os.Stderr, "fakeagent: nothing to play under the name %q\n", name)
		return 2
	}
	enc := json.NewEncoder(stdout)
	_ = enc.Encode(obj{"type": "system", "subtype": "init", "model": "fake"})
	text, err := play(cmp.Or(os.Getenv("FAKEAGENT_SCENARIO"), "fixing"), brief)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}
	if out := argAfter(args, "-o"); name == "codex" && out != "" {
		if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 1
		}
	}
	_ = enc.Encode(final(text))
	return 0
}

func record(name string, args []string, brief string) error {
	path := os.Getenv("FAKEAGENT_LOG")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(obj{"name": name, "args": args, "brief": brief})
}

func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
