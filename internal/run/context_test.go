package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The usage on every model call of pi's and claude's streams reaches the screen, with the window pi's models.json
// gives its model and none for claude, whose stream names no window.
func TestContextUsedMatchesItsEventGoldens(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join("testdata", "pi-agent"))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH")) // the fake pi, whose table lists nothing
	for _, harness := range []string{"pi", "claude"} {
		b, err := json.Marshal(activityEvents(harness, fixture(t, harness+"-context.jsonl")))
		var got []map[string]any
		if err != nil || json.Unmarshal(b, &got) != nil {
			t.Fatal(err)
		}
		if want := goldenEvents(t, harness+"-context.events.jsonl"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s events:\n%v\nwant:\n%v", harness, got, want)
		}
	}
}

// A model pi's models.json does not list, or no models.json, has no window: never a guessed one.
func TestContextUsedWithoutAPiWindow(t *testing.T) {
	line := []byte(`{"type":"message_end","message":{"role":"assistant","provider":"llama-cpp","model":"other",` +
		`"usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0}}}`)
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, dir := range map[string]string{"unlisted": filepath.Join("testdata", "pi-agent"), "none": t.TempDir()} {
		t.Setenv("PI_CODING_AGENT_DIR", dir)
		got := contextUsed("pi", line)
		if len(got) != 1 || got[0]["context"].(map[string]any)["used"] != 15 ||
			got[0]["context"].(map[string]any)["window"] != 0 {
			t.Fatalf("%s: want 15 used and no window, got %v", name, got)
		}
	}
}

// pi's listed figures are tokens in thousands or millions; what is no figure has no window.
func TestListedTokens(t *testing.T) {
	for in, want := range map[string]int{"500K": 500000, "272K": 272000, "1M": 1000000, "1.5M": 1500000,
		"4096": 4096, "": 0, "-": 0, "yes": 0} {
		if got := listedTokens(in); got != want {
			t.Errorf("listedTokens(%q) = %d, want %d", in, got, want)
		}
	}
}
