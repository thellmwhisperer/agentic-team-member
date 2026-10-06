// Package duration is the one way every ATM surface prints a duration (#150 point 12): 0.9 s, 4 min 28 s,
// 1 h 02 min. Never raw seconds.
package duration

import (
	"fmt"
	"time"
)

// Format is d for a human: tenths under a second, then seconds, minutes and seconds, hours and minutes.
func Format(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %02d s", s/60, s%60)
	}
	return fmt.Sprintf("%d h %02d min", s/3600, s%3600/60)
}
