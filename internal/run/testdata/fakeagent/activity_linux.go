package main

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func init() {
	if len(os.Args) == 4 && os.Args[1] == "--quiet-work" && os.Args[2] == "private" {
		if unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil ||
			os.WriteFile(os.Args[3]+".pid", []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			os.Exit(1)
		}
	}
}
