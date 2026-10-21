//go:build unix

package main

import (
	"encoding/json"
	"os"
	"syscall"
	"time"
)

func quietWork(mode, receipt string) error {
	f, err := os.Create(receipt + ".data")
	if err != nil {
		return err
	}
	defer f.Close()
	block := make([]byte, 1024*1024)
	var samples []obj
	start := time.Now()
	var transferred int
	for range 25 {
		if mode == "cpu" {
			for end := time.Now().Add(5 * time.Millisecond); time.Now().Before(end); {
			}
		} else {
			n, err := f.WriteAt(block, 0)
			if err != nil {
				return err
			}
			transferred += n
			if err := f.Sync(); err != nil {
				return err
			}
			n, err = f.ReadAt(block, 0)
			if err != nil {
				return err
			}
			transferred += n
		}
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			return err
		}
		samples = append(samples, obj{"pid": os.Getpid(), "cpu_ns": usage.Utime.Nano() + usage.Stime.Nano(),
			"bytes": transferred})
		time.Sleep(200 * time.Millisecond)
	}
	b, err := json.Marshal(obj{"elapsed_ns": time.Since(start).Nanoseconds(), "samples": samples})
	if err != nil {
		return err
	}
	return os.WriteFile(receipt, b, 0o600)
}
