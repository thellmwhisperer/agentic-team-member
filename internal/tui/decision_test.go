package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestApprovedCheckClearsItsFinding(t *testing.T) {
	m := screen(100, 24,
		`{"step":"checks","check":"test","state":"failed","duration_ms":100}`,
		`{"step":"checks","state":"failed","error":"test: exit status 2","duration_ms":200}`,
		`{"step":"decision","state":"waiting","message":"Check failed\nAnswer: atm answer issue-1"}`,
		`{"step":"decision","state":"answered","node":"checks","answer":"approve","by":"operator"}`)
	m.end(run.Outcome{Outcome: "passed"}, nil)
	s := view(m)
	if strings.Contains(s, "Findings") || strings.Contains(s, "✗") ||
		!regexp.MustCompile(`✓ Checks  0.2 s +approved by operator`).MatchString(s) ||
		!regexp.MustCompile(`✓ test  0.1 s +approved`).MatchString(s) {
		t.Fatalf("approved check retains a failure or truncates its label:\n%s", s)
	}
	t.Logf("approved screen:\n%s", s)
}
