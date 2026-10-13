package cli

import "testing"

func TestFollowFlagLeavesOtherFlagValuesAlone(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"7", "--follow"}, 1},
		{[]string{"--harness-arg", "--follow", "7"}, -1},
		{[]string{"--harness-arg", "--follow", "7", "--follow"}, 3},
		{[]string{"--harness-arg=--follow", "--follow", "7"}, 1},
		{[]string{"--", "--follow"}, -1},
	} {
		if got := followArg(c.args); got != c.want {
			t.Errorf("followArg(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}
