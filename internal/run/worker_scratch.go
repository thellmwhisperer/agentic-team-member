package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const workerScratchRetention = 7 * 24 * time.Hour

// Both snapshots come from the server's lock, so a newly started run cannot look archived.
func sweepWorkerScratch(runs string, entries []os.DirEntry, outcomes map[string]Outcome) {
	cutoff := time.Now().Add(-workerScratchRetention)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(runs, entry.Name())
		run, listed := outcomes[entry.Name()]
		if !listed {
			if _, err := os.Lstat(filepath.Join(dir, "worker-scratch")); os.IsNotExist(err) {
				continue
			}
			run = archivedScratchOutcome(dir)
		}
		if !run.Ended.IsZero() && (run.Outcome == "passed" ||
			run.Outcome == "failed" && (run.Reason == errStopped.Error() || run.Ended.Before(cutoff))) {
			removeWorkerScratch(dir)
		}
	}
}

// Runs trimmed from runs.jsonl still have the report written at their end.
func archivedScratchOutcome(dir string) Outcome {
	path := filepath.Join(dir, "report.json")
	info, err := os.Stat(path)
	if err != nil {
		return Outcome{}
	}
	b, err := os.ReadFile(path)
	var r verdict
	if err != nil || json.Unmarshal(b, &r) != nil ||
		slices.ContainsFunc(r.Decisions, func(d Decision) bool { return d.Answer == "" }) {
		return Outcome{}
	}
	if r.FailedNode != "" || r.Reason == errStopped.Error() {
		return Outcome{Outcome: "failed", Reason: r.Reason, Ended: info.ModTime()}
	}
	if len(r.Units) != 0 && slices.ContainsFunc(r.Nodes, func(n *node) bool {
		return n != nil && n.Name == "delivery" && (n.Result == "passed" || n.Result == "skipped")
	}) {
		return Outcome{Outcome: "passed", Ended: info.ModTime()}
	}
	return Outcome{}
}

func removeWorkerScratch(dir string) {
	if err := removeScratch(dir, "worker-scratch"); !os.IsNotExist(err) {
		recordScratchCleanup(dir, err)
	}
}

func recordScratchCleanup(dir string, err error) {
	f, openErr := os.OpenFile(filepath.Join(dir, "scratch-cleanup.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if openErr == nil {
		defer func() { _ = f.Close() }()
		_, _ = fmt.Fprintln(f, time.Now().UTC().Format(time.RFC3339), outcome(err), err)
	}
}
