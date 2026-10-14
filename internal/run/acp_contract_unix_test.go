//go:build unix

package run

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hermesTranscript is the agent's side of a recorded hermes --ignore-rules acp run of 13 Oct, after its replies to
// initialize and session/new, before its prompt's reply.
var hermesTranscript = sessionUpdate(`{"content":{"text":"**Executing shell command to create file**",`+
	`"type":"text"},"messageId":"m1","sessionUpdate":"agent_thought_chunk"}`) +
	sessionUpdate(`{"content":[{"content":{"text":"$ cat nope.txt; printf 'hola' > hola.txt","type":"text"},`+
		`"type":"content"}],"kind":"execute","locations":[],"title":"terminal: cat nope.txt + 1 command",`+
		`"toolCallId":"tc-1","sessionUpdate":"tool_call"}`) +
	sessionUpdate(`{"content":[{"content":{"text":"terminal result\n- **output:** cat: nope.txt: No such file `+
		`or directory\n- **exit_code:** 0","type":"text"},"type":"content"}],"kind":"execute","status":"completed",`+
		`"toolCallId":"tc-1","sessionUpdate":"tool_call_update"}`) +
	sessionUpdate(`{"content":{"text":"**Verifying file reading function**","type":"text"},"messageId":"m2",`+
		`"sessionUpdate":"agent_thought_chunk"}`) +
	sessionUpdate(`{"kind":"read","locations":[{"path":"<clone>/hola.txt"}],"title":"read_file: hola.txt",`+
		`"toolCallId":"tc-2","sessionUpdate":"tool_call"}`) +
	sessionUpdate(`{"content":{"text":"\n\nDONE","type":"text"},"messageId":"m3",`+
		`"sessionUpdate":"agent_message_chunk"}`)

// sessionUpdate is the line of a session/update notification of update.
func sessionUpdate(update string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":` + update + "}}\n"
}

// init plays hermes when this test binary runs under that name: an ACP agent on its stdin and stdout, as
// $FAKE_ACP says. It answers initialize and session/new, does the brief's work, replays $FAKE_ACP_STREAM, sends
// $FAKE_ACP_REQUEST and keeps ATM's reply, says its report and replies $FAKE_ACP_STOP, end_turn by default. How
// it was called goes to $FAKE_ACP_CALL. $FAKE_ACP_FAIL, method:message, answers that call with an error; silent
// says nothing on stdout, die writes boom to stderr and exits 3, no-skill leaves skill out of its report.
func init() {
	if filepath.Base(os.Args[0]) == "hermes" {
		os.Exit(fakeHermes())
	}
}

func fakeHermes() int {
	mode := os.Getenv("FAKE_ACP")
	wd, _ := os.Getwd()
	clone := func(s string) string { return strings.ReplaceAll(s, "<clone>", wd) }
	fmt.Fprintln(os.Stderr, "hermes stderr")
	if mode == "die" {
		fmt.Fprintln(os.Stderr, "boom")
		return 3
	}
	_, tty := os.Open("/dev/tty")
	f := fakeACP{in: bufio.NewScanner(os.Stdin), call: obj{"args": os.Args[1:], "tty": tty == nil},
		save: os.Getenv("FAKE_ACP_CALL")}
	f.in.Buffer(nil, 1<<24)
	f.fail, f.message, _ = strings.Cut(os.Getenv("FAKE_ACP_FAIL"), ":")
	defer func() {
		for f.in.Scan() { // until ATM stops it
		}
	}()
	if mode == "silent" || !f.answer(f.read(), obj{"protocolVersion": 1, "agentCapabilities": obj{}}) ||
		!f.answer(f.read(), obj{"sessionId": "s1"}) {
		return 0
	}
	prompt := f.read()
	_, text := promptText(prompt["params"])
	report, review := fakeHermesWork(text, mode)
	if review {
		f.save += ".ponytail"
	}
	fmt.Print(clone(os.Getenv("FAKE_ACP_STREAM")))
	if request := os.Getenv("FAKE_ACP_REQUEST"); request != "" {
		fmt.Println(clone(request))
		f.call["reply"] = f.read()
	}
	emit(obj{"jsonrpc": "2.0", "method": "session/update", "params": obj{"sessionId": "s1", "update": obj{
		"sessionUpdate": "agent_message_chunk", "messageId": "report",
		"content": obj{"type": "text", "text": "Done.\n" + report + "}"}}}})
	f.answer(prompt, obj{"stopReason": cmp.Or(os.Getenv("FAKE_ACP_STOP"), "end_turn")})
	return 0
}

// fakeACP is the fake hermes's side of the conversation: what it reads, what it keeps of the calls and where,
// and the call it fails, with message.
type fakeACP struct {
	in                  *bufio.Scanner
	call                obj
	save, fail, message string
}

func (f fakeACP) read() (m obj) {
	f.in.Scan()
	_ = json.Unmarshal(f.in.Bytes(), &m)
	return m
}

// answer keeps call m and answers it with result, or with an error when it is the call to fail.
func (f fakeACP) answer(m obj, result obj) bool {
	method, _ := m["method"].(string)
	f.call[method] = m["params"]
	if method == f.fail || method == "session/prompt" { // before the answer, which ends the fake
		b, _ := json.Marshal(f.call)
		_ = os.WriteFile(f.save, b, 0o600)
	}
	if method == f.fail {
		emit(obj{"jsonrpc": "2.0", "id": m["id"], "error": obj{"code": -32000, "message": f.message}})
		return false
	}
	emit(obj{"jsonrpc": "2.0", "id": m["id"], "result": result})
	return true
}

// fakeHermesWork does the work of the brief whose text is prompt, none for the ponytail pass, and returns its
// report, without its closing brace, with the skill the prompt holds unless mode is no-skill, and whether it was
// the ponytail pass.
func fakeHermesWork(prompt, mode string) (string, bool) {
	skill, report := "ponytail", `{"test_file": "a_test.sh", "changed_files": ["a.txt"], "summary": "s", `+
		`"commands_run": [], "follow_ups": []`
	review := strings.Contains(prompt, "`ponytail-review`")
	if review {
		skill, report = "ponytail-review", `{"findings": [], "summary": "Lean already."`
	} else if exec.Command("sh", "-c", fixWork).Run() != nil {
		report = "no work"
	}
	if strings.Contains(prompt, "name: "+skill+"\n") && mode != "no-skill" {
		report += `, "skill": "` + skill + `"`
	}
	return report, review
}

// hermesRun runs the issue with harness hermes, the fake above first on PATH, env set, flags before the issue,
// and returns what a screen watching it saw, the repository, where the fake's call went, and the run's error.
func hermesRun(t *testing.T, env map[string]string, flags ...string) (*watched, string, string, error) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err == nil {
		err = os.Symlink(exe, filepath.Join(bin, "hermes"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	call := filepath.Join(t.TempDir(), "call.json")
	t.Setenv("FAKE_ACP_CALL", call)
	for k, v := range env {
		t.Setenv(k, v)
	}
	var w watched
	err = Run("t", append(flags, "--harness", "hermes", issueFile(t, issue)), &w, io.Discard)
	return &w, root, call, err
}

// hermesCall is what the fake hermes kept of how ATM called it.
func hermesCall(t *testing.T, path string) obj {
	t.Helper()
	var call obj
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &call)
	}
	if err != nil {
		t.Fatal("hermes was not called: ", err)
	}
	return call
}

// boxLines is what the agent's box shows of a run: its thoughts, its messages and each tool event.
func boxLines(evs []obj) (lines []string) {
	for _, ev := range evs {
		switch {
		case ev["step"] != "agent":
		case ev["thinking"] != nil:
			lines = append(lines, "∴ "+ev["thinking"].(string))
		case ev["says"] != nil:
			lines = append(lines, "✎ "+ev["says"].(string))
		case ev["id"] != nil:
			lines = append(lines, strings.Join([]string{"tool", ev["id"].(string), ev["tool"].(string),
				ev["detail"].(string), ev["result"].(string)}, " "))
		}
	}
	return lines
}

func TestHermesSpeaksACPItselfWithoutATerminal(t *testing.T) {
	w, root, path, err := hermesRun(t, map[string]string{"FAKE_ACP_STREAM": hermesTranscript})
	if err != nil {
		t.Fatal(err)
	}
	end, clone, _ := agentStep(t, &w.Buffer)
	if end["state"] != "passed" {
		t.Fatalf("agent step: %v", end)
	}
	want := []string{"∴ **Executing shell command to create file**",
		"tool tc-1 execute terminal: cat nope.txt + 1 command running", "tool tc-1   passed",
		"∴ **Verifying file reading function**", "tool tc-2 read read_file: hola.txt running", "✎ DONE", "✎ Done."}
	if got := boxLines(w.live); !slices.Equal(got[:min(len(got), len(want))], want) {
		t.Fatalf("box lines:\n%s\nwant first\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	call := hermesCall(t, path)
	if !reflect.DeepEqual(call["args"], []any{"--ignore-rules", "acp"}) || call["tty"] != false {
		t.Fatalf("want hermes --ignore-rules acp with no controlling terminal, got %v", call)
	}
	assertHermesConversation(t, call, root, clone)
}

// assertHermesConversation checks ATM's calls of the conversation hermes kept in call, in a run of the
// repository root in clone.
func assertHermesConversation(t *testing.T, call obj, root, clone string) {
	t.Helper()
	initialize, _ := call["initialize"].(map[string]any)
	caps, _ := initialize["clientCapabilities"].(map[string]any)
	if initialize["protocolVersion"] != 1.0 || caps == nil || caps["fs"] != nil || caps["terminal"] != nil {
		t.Fatalf("initialize: %v, want protocol version 1, no fs, no terminal", initialize)
	}
	session, _ := call["session/new"].(map[string]any)
	if session["cwd"] != clone || !reflect.DeepEqual(session["mcpServers"], []any{}) {
		t.Fatalf("session/new: %v, want cwd %s and no MCP servers", session, clone)
	}
	brief, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief.md"))
	prompt, _ := call["session/prompt"].(map[string]any)
	if blocks, text := promptText(prompt); prompt["sessionId"] != "s1" || blocks != 1 ||
		!strings.Contains(text, string(brief)) || !strings.Contains(text, "name: ponytail\n") {
		t.Fatalf("session/prompt: want the brief and the ponytail skill as one text block, got %v", prompt)
	}
}

// promptText is how many blocks a session/prompt's params have, and the text of the first, a text block.
func promptText(params any) (int, string) {
	p, _ := params.(map[string]any)
	blocks, _ := p["prompt"].([]any)
	if len(blocks) == 0 {
		return 0, ""
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" {
		return len(blocks), ""
	}
	text, _ := block["text"].(string)
	return len(blocks), text
}

// permissionRequest asks permission for a tool call with locations and rawInput, an always option first.
func permissionRequest(locations, rawInput string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1",` +
		`"toolCall":{"toolCallId":"p1","kind":"edit","title":"write","locations":` + locations +
		`,"rawInput":` + rawInput + `},"options":[{"optionId":"aa","name":"Always","kind":"allow_always"},` +
		`{"optionId":"ao","name":"Once","kind":"allow_once"},{"optionId":"ra","name":"Never","kind":"reject_always"},` +
		`{"optionId":"ro","name":"No","kind":"reject_once"}]}}`
}

func TestHermesPermissionIsOnceAndOnlyInsideTheClone(t *testing.T) {
	for _, c := range []struct{ name, request, option string }{
		{"inside", permissionRequest(`[{"path":"<clone>/a.txt"}]`, `{"path":"<clone>/a.txt"}`), "ao"},
		{"outside", permissionRequest(`[{"path":"/etc/hosts"}]`, `{}`), "ro"},
		{"outside in rawInput", permissionRequest(`[]`, `{"file_path":"/etc/hosts"}`), "ro"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, path, err := hermesRun(t, map[string]string{"FAKE_ACP_REQUEST": c.request})
			if err != nil {
				t.Fatal(err)
			}
			want := obj{"jsonrpc": "2.0", "id": 7.0, "result": obj{"outcome": obj{"outcome": "selected",
				"optionId": c.option}}}
			if got := hermesCall(t, path)["reply"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("permission answer %v, want %v", got, want)
			}
			_, _, log := agentStep(t, &w.Buffer)
			if !slices.ContainsFunc(logLines(t, log), func(l string) bool {
				return strings.Contains(l, `"optionId":"`+c.option+`"`)
			}) {
				t.Fatalf("the answer is not a line of the agent's log %s", log)
			}
		})
	}
}

func TestHermesStepEndsWithItsPromptsReply(t *testing.T) {
	for _, c := range []struct{ name, env, value, why string }{
		{"max_tokens", "FAKE_ACP_STOP", "max_tokens", "max_tokens"},
		{"refusal", "FAKE_ACP_STOP", "refusal", "refusal"},
		{"cancelled", "FAKE_ACP_STOP", "cancelled", "cancelled"},
		{"initialize error", "FAKE_ACP_FAIL", "initialize:bad protocol", "bad protocol"},
		{"session/new error", "FAKE_ACP_FAIL", "session/new:no sessions here", "no sessions here"},
		{"prompt error", "FAKE_ACP_FAIL", "session/prompt:rate limited", "rate limited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, _, err := hermesRun(t, map[string]string{c.env: c.value})
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want the step failed with %q, got %v", c.why, err)
			}
			if end, _, _ := agentStep(t, &w.Buffer); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
		})
	}
}

func TestHermesGetsMethodNotFoundForWhatATMDidNotOffer(t *testing.T) {
	request := `{"jsonrpc":"2.0","id":"r1","method":"fs/read_text_file",` +
		`"params":{"sessionId":"s1","path":"<clone>/a.txt"}}`
	_, _, path, err := hermesRun(t, map[string]string{"FAKE_ACP_REQUEST": request})
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := hermesCall(t, path)["reply"].(map[string]any)
	e, _ := reply["error"].(map[string]any)
	if reply["id"] != "r1" || e["code"] != -32601.0 || e["message"] != "method not found" {
		t.Fatalf("reply %v, want -32601 method not found", reply)
	}
}

func TestHermesTakesNoModelOrEffort(t *testing.T) {
	for _, flags := range [][]string{{"--effort", "high"}, {"--model", "m"}} {
		t.Run(flags[0], func(t *testing.T) {
			w, root, _, err := hermesRun(t, nil, flags...)
			want := "hermes takes model and effort from its own config; unset them in .atm.yaml"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %s", err, want)
			}
			if strings.Contains(w.String(), `"step":"clone"`) || len(clones(t, root)) != 0 {
				t.Fatalf("clone ran: %s", w)
			}
		})
	}
}

func TestHermesProvesItsSkillInItsReport(t *testing.T) {
	w, _, _, err := hermesRun(t, map[string]string{"FAKE_ACP": "no-skill"})
	if err == nil || !strings.Contains(err.Error(), "agent left no proof it used the ponytail skill") {
		t.Fatalf("got %v", err)
	}
	if end, _, _ := agentStep(t, &w.Buffer); end["state"] != "failed" {
		t.Fatalf("end event: %v", end)
	}
}

func TestHermesFetchInRawInputReachesOutside(t *testing.T) {
	update := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"kind":"execute",` +
		`"title":"terminal","toolCallId":"f1","rawInput":{"command":"curl -s example.com"},"sessionUpdate":"tool_call"}}}` +
		"\n"
	_, _, _, err := hermesRun(t, map[string]string{"FAKE_ACP_STREAM": update})
	if want := "the agent reached outside its clone: execute"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
}

func TestHermesDeathKeepsItsCauseAndTouchesNoOtherRun(t *testing.T) {
	defer func(d time.Duration) { agentSilence = d }(agentSilence)
	agentSilence = time.Second
	for _, c := range []struct{ mode, exit, why, tail string }{
		{"silent", "silent for 1.0 s", "the agent said nothing for 1.0 s; ATM stopped it", "hermes stderr"},
		{"die", "exit code 3", "exit code 3", "boom"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			other := exec.Command(os.Args[0]) // another run's hermes, which this run must not touch
			other.Env = append(os.Environ(), "FAKE_ACP=silent")
			other.Args[0] = "hermes"
			_, err := other.StdinPipe() // open until the test ends
			if err == nil {
				err = other.Start()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
			w, root, _, err := hermesRun(t, map[string]string{"FAKE_ACP": c.mode})
			end, _, log := agentStep(t, &w.Buffer)
			if want := c.why + "; log: " + log; err == nil || err.Error() != want || end["error"] != want {
				t.Fatalf("want %q, got %v", want, err)
			}
			n := reportNode(t, readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json")), "agent")
			if tail, _ := n["tail"].(string); n["exit"] != c.exit || !strings.Contains(tail, c.tail) {
				t.Fatalf("want exit %q and a tail holding %q, got %v", c.exit, c.tail, n)
			}
			if stderr, _ := os.ReadFile(strings.TrimSuffix(log, ".jsonl") + ".stderr.txt"); !bytes.Contains(stderr,
				[]byte("hermes stderr")) {
				t.Fatalf("stderr: %q", stderr)
			}
			var status syscall.WaitStatus
			if pid, _ := syscall.Wait4(other.Process.Pid, &status, syscall.WNOHANG, nil); pid != 0 {
				t.Fatal("stopping this run's agent ended another hermes: ", status)
			}
		})
	}
}
