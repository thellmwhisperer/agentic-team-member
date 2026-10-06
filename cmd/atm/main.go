// atm is the Go port of ATM (#150): one binary in the PATH, run from inside the repository it works on.
package main

import (
	"fmt"
	"os"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: atm run [--base-ref r] [--harness h] [--model m] [--effort e] "+
			"<issue.md | issue number>")
		os.Exit(2)
	}
	if err := run.Run(os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "atm:", err)
		os.Exit(2)
	}
}
