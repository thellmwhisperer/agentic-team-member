package run

import (
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
	activity := fields[11] + " " + fields[12]
	io, err := os.ReadFile(base + "/io")
	for line := range strings.Lines(string(io)) {
		key, _, _ := strings.Cut(line, ":")
		switch key {
		case "rchar", "wchar", "read_bytes", "write_bytes":
			activity += " " + line
		}
	}
	return activity, err
}
