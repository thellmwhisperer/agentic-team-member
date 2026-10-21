package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func eventGolden(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("../run/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func modelWithEvents(width int, events []string) *Model {
	m := New("issue-1", width, 40)
	for _, event := range append([]string{`{"step":"agent","state":"started"}`}, events...) {
		m.apply(run.Frame{Event: []byte(event)})
	}
	return m
}

// agentLines is the Agent box's rows of m, without the blank ones that fill the box above them.
func agentLines(m *Model, width int) []string {
	var lines []string
	for _, line := range strings.Split(ansi.Strip(m.agentBox(width, 40)), "\n") {
		if line = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")); line != "" &&
			!strings.HasPrefix(line, "╭") && !strings.HasPrefix(line, "╰") {
			lines = append(lines, line)
		}
	}
	return lines
}

func checkAgentBox(t *testing.T, events, want []string, tools int) {
	t.Helper()
	m := modelWithEvents(240, events)
	if got := agentLines(m, 240); !reflect.DeepEqual(got, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", got, want)
	}
	m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"passed","unit":1,"duration_ms":1000}`)})
	if got := view(m); !strings.Contains(got, "✓ Agent  1.0 s") ||
		!strings.Contains(got, fmt.Sprintf("%d tools · passed", tools)) {
		t.Fatalf("Agent tool count note:\n%s", got)
	}
}

func TestAgentBoxRendersRunParserEventGoldens(t *testing.T) {
	checkAgentBox(t, eventGolden(t, "pi-6-2.events.jsonl"), []string{
		"✎ I'll start by reading the project instructions and the ponytail skill, then look at the analytics package.",
		"▸ · 1 tool: read",
		"▸ ∴ Exploring the packages directory, particularly the analytics package, to understand existing patterns.",
		"▸ ⠋ 1 tool: bash · ls packages",
	}, 2)
	checkAgentBox(t, eventGolden(t, "claude-261.events.jsonl"), []string{
		"✎ Writing the red tests first, then the fix in reset.",
		"▸ · 1 tool: Edit",
	}, 1)
}

func TestAgentBoxRendersCursorToolStartOrder(t *testing.T) {
	checkAgentBox(t, eventGolden(t, "cursor.events.jsonl"), []string{
		"▸ ∴ I'll run the failing command, read hello.txt, and reply DONE.",
		"✎ I'll run the failing cat, then read hello.txt.",
		"✗ failed shell cat nope.txt · cat: nope.txt: No such file or directory",
		"▸ · 1 tool: read",
		"▸ ∴ The first command failed as expected.",
		"✎ DONE",
	}, 2)
}

func TestAgentBoxRendersTheSameStoryForBothHarnesses(t *testing.T) {
	want := []string{
		"▸ ∴ Exploring the packages directory, particularly the analytics package, to understand existing patterns.",
		"✎ Writing the red tests first, then the fix in reset.",
		"▸ · 1 tool: Edit",
	}
	for _, name := range []string{"same-story-claude.events.jsonl", "same-story-pi.events.jsonl"} {
		checkAgentBox(t, eventGolden(t, name), want, 1)
	}
}

// agentBoxSays is the Agent box, width wide, once the agent says text.
func agentBoxSays(t *testing.T, width int, text string) (string, []string) {
	t.Helper()
	b, err := json.Marshal(map[string]string{"says": text})
	if err != nil {
		t.Fatal(err)
	}
	m := modelWithEvents(width, []string{string(b)})
	return m.agentBox(width, 40), agentLines(m, width)
}

func requireAgentBoxContains(t *testing.T, width int, text, want string) string {
	t.Helper()
	raw, _ := agentBoxSays(t, width, text)
	if stripped := ansi.Strip(raw); !strings.Contains(stripped, want) {
		t.Fatalf("rendered line is missing %q: %q", want, stripped)
	}
	return raw
}

func activeStylesAtText(raw, text string) (bool, bool, bool) {
	bold, italic := false, false
	offset := 0
	for _, sgr := range regexp.MustCompile(`\x1b\[([0-9;]*)m`).FindAllStringSubmatchIndex(raw, -1) {
		if strings.Contains(raw[offset:sgr[0]], text) {
			return bold, italic, true
		}
		for _, code := range strings.Split(raw[sgr[2]:sgr[3]], ";") {
			switch code {
			case "0":
				bold, italic = false, false
			case "1":
				bold = true
			case "3":
				italic = true
			case "22":
				bold = false
			case "23":
				italic = false
			}
		}
		offset = sgr[1]
	}
	if strings.Contains(raw[offset:], text) {
		return bold, italic, true
	}
	return false, false, false
}

func TestAgentBoxRendersMarkdownBold(t *testing.T) {
	raw, lines := agentBoxSays(t, 60, "**Evaluating helper fallback**")
	if want := []string{"✎ Evaluating helper fallback"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	if !strings.Contains(raw, "\x1b[1mEvaluating") {
		t.Fatalf("bold text is not bold:\n%q", raw)
	}
}

func TestAgentBoxRendersUnderscoreAndVariableBacktickMarkdown(t *testing.T) {
	raw, lines := agentBoxSays(t, 100, "__Bold__ and _emphasis_ with ``a `tick` and  double spaces``.")
	if want := []string{"✎ Bold and emphasis with a `tick` and  double spaces."}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	if !strings.Contains(raw, "\x1b[1mBold") || !strings.Contains(raw, "\x1b[3memphasis") {
		t.Fatalf("Markdown styles are missing:\n%q", raw)
	}
}

func TestAgentBoxRendersNestedAndTripleEmphasis(t *testing.T) {
	for _, text := range []string{"**bold _italic_**", "- ***bold italic***"} {
		raw, lines := agentBoxSays(t, 60, text)
		want := "✎ bold italic"
		if strings.HasPrefix(text, "-") {
			want = "✎ • bold italic"
		}
		if !reflect.DeepEqual(lines, []string{want}) {
			t.Fatalf("Agent box lines for %q:\n%q\nwant:\n%q", text, lines, []string{want})
		}
		styleWants := map[string][2]bool{"bold": {true, false}, "italic": {true, true}}
		if strings.HasPrefix(text, "-") {
			styleWants = map[string][2]bool{"bold": {true, true}, "italic": {true, true}}
		}
		for word, styles := range styleWants {
			bold, italic, found := activeStylesAtText(raw, word)
			if !found || bold != styles[0] || italic != styles[1] {
				t.Fatalf("wrong active styles for %q in %q: bold=%t italic=%t found=%t\n%q", word, text, bold, italic, found, raw)
			}
		}
	}
}

func TestAgentBoxKeepsFencedMarkdownLiteralAndWrapsAtPanelWidth(t *testing.T) {
	const width = 32
	raw, lines := agentBoxSays(t, width, "```\n**literal**\n- x\n\t123456789012345678901234567890123456\n```")
	want := []string{"✎ **literal**", "- x", "123456789012345678", "901234567890123456"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("fenced code changed:\n%q\nwant:\n%q", lines, want)
	}
	for _, line := range strings.Split(raw, "\n") {
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("rendered line is %d columns, want %d: %q", got, width, ansi.Strip(line))
		}
	}
	if strings.Contains(raw, "\t") {
		t.Fatalf("tab reached terminal output: %q", raw)
	}
}

func TestAgentBoxRendersAutoLinksAndHTMLSourceInListsAndText(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "autolink", text: "<https://example.org>", want: "https://example.org"},
		{name: "autolink in list", text: "- <https://example.org>", want: "• https://example.org"},
		{name: "HTML block", text: "<div>text</div>", want: "<div>text</div>"},
		{name: "HTML in list", text: "- <div>text</div>", want: "• <div>text</div>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := agentBoxSays(t, 60, tc.text)
			if stripped := ansi.Strip(raw); !strings.Contains(stripped, tc.want) {
				t.Fatalf("rendered source is missing %q: %q", tc.want, stripped)
			}
		})
	}
}

func TestAgentBoxClassifiesIndentedCodeBeforeListMarkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "four-space code", text: "    - x", want: "✎     - x"},
		{name: "tab-indented code", text: "\t- x", want: "✎         - x"},
		{name: "valid one-space list", text: " - x", want: "✎  • x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := requireAgentBoxContains(t, 60, tc.text, tc.want)
			if strings.Contains(raw, "\t") {
				t.Fatalf("tab reached terminal output: %q", raw)
			}
		})
	}
}

func TestAgentBoxPreservesInlineCodeWhitespaceWhenWrapping(t *testing.T) {
	for _, text := range []string{
		"`abcdefghijklmnopqrst  abcdefghijklmnop`",
		"- `abcdefghijklmnopqrst  abcdefghijklmnop`",
	} {
		raw, _ := agentBoxSays(t, 32, text)
		var rendered []string
		for _, line := range strings.Split(ansi.Strip(raw), "\n") {
			if !strings.HasPrefix(line, "│ ") {
				continue
			}
			line = strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│ "), " │"), " ")
			if line == "" {
				continue
			}
			if len(rendered) > 0 {
				line = strings.TrimPrefix(line, "  ")
				if strings.HasPrefix(text, "-") {
					line = strings.TrimPrefix(line, "  ")
				}
			}
			if len(rendered) == 0 {
				line = strings.TrimPrefix(line, "✎ ")
				line = strings.TrimPrefix(line, "• ")
			}
			rendered = append(rendered, line)
		}
		if got := strings.Join(rendered, ""); got != "abcdefghijklmnopqrst  abcdefghijklmnop" {
			t.Fatalf("inline code changed while wrapping %q: %q", text, got)
		}
	}
}

func TestAgentBoxPreservesCodeSourceWhitespaceAndExpandsTabs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "indented block", text: "    x\tz", want: "✎     x   z"},
		{name: "inline code padding", text: "` x `", want: "✎  x "},
		{name: "inline code tab", text: "`x\tz`", want: "x       z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireAgentBoxContains(t, 60, tc.text, tc.want)
		})
	}
}

func TestAgentBoxOnlyClosesFenceWithMatchingMarkerAndLength(t *testing.T) {
	raw, lines := agentBoxSays(t, 60, "````go\n~~~\n```\n  kept  \n````\nafter")
	if want := []string{"✎ ~~~", "```", "kept", "after"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	if !strings.Contains(ansi.Strip(raw), "  kept  ") {
		t.Fatalf("fenced code whitespace changed:\n%s", ansi.Strip(raw))
	}
}

func TestAgentBoxRendersMarkdownWithinANarrowPanel(t *testing.T) {
	const width = 32
	raw, lines := agentBoxSays(t, width, "Plan for *the* fix:\n- read `view.go` and the tests around it\n"+
		"- fix\n\n```go\nif  x {\n\treturn 1 // a long code line that has to wrap\n}\n```\nDone.")
	want := []string{
		"✎ Plan for the fix:",
		"• read view.go and the",
		"tests around it",
		"• fix",
		"if  x {",
		"return 1 // a long",
		"code line that has to wra",
		"p",
		"}",
		"Done.",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	for _, l := range strings.Split(raw, "\n") {
		if w := ansi.StringWidth(l); w != width {
			t.Fatalf("line %q is %d wide, want %d", ansi.Strip(l), w, width)
		}
	}
	if stripped := ansi.Strip(raw); !strings.Contains(stripped, "│           return 1") ||
		!strings.Contains(stripped, "│      tests around it") {
		t.Fatalf("indentation not kept:\n%s", stripped)
	}
}

func TestAgentBoxDoesNotPassTerminalControlsThrough(t *testing.T) {
	raw, lines := agentBoxSays(t, 60, "safe\x1b]0;title\a \x1b[2Jtext\r\b")
	if want := []string{"✎ safe text"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	for _, c := range []string{"\x1b]", "\x1b[2J", "\a", "\r", "\b"} {
		if strings.Contains(raw, c) {
			t.Fatalf("Agent box passes %q through:\n%q", c, raw)
		}
	}
}

func TestAgentBoxDoesNotDecodeMarkdownEntitiesIntoTerminalControls(t *testing.T) {
	raw, lines := agentBoxSays(t, 60, "safe&#x1b;[2Jtext")
	if want := []string{"✎ safe[2Jtext"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", lines, want)
	}
	if strings.Contains(raw, "\x1b[2J") {
		t.Fatalf("Agent box emitted an entity-decoded terminal control:\n%q", raw)
	}
}
