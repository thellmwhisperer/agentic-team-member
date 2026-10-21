package run

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processActivity(pid int) (string, error) {
	base := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(base + "/stat")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
	if len(fields) < 13 {
		return "", fmt.Errorf("incomplete process stat")
	}
	io, err := os.ReadFile(base + "/io")
	if err != nil {
		if _, statErr := os.Stat(base); os.IsNotExist(statErr) {
			return "", statErr
		}
		return "", fmt.Errorf("read process I/O counters: %v", err)
	}
	activity := fields[11] + " " + fields[12]
	for line := range strings.Lines(string(io)) {
		key, _, _ := strings.Cut(line, ":")
		switch key {
		case "rchar", "wchar", "read_bytes", "write_bytes":
			activity += " " + line
		}
	}
	return activity, nil
}
