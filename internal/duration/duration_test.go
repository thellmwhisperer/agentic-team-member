package duration

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	for d, want := range map[time.Duration]string{
		900 * time.Millisecond:                  "0.9 s",
		7 * time.Second:                         "7 s",
		4*time.Minute + 28*time.Second:          "4 min 28 s",
		14*time.Minute + 1270*time.Millisecond:  "14 min 01 s",
		time.Hour + 2*time.Minute + time.Second: "1 h 02 min",
	} {
		if got := Format(d); got != want {
			t.Errorf("Format(%v) = %q, want %q", d, got, want)
		}
	}
}
