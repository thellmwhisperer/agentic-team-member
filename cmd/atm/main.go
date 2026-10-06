// atm is the Go port of ATM (#150). The behaviour it owes is the contract in internal/e2e.
package main

import (
	"os"

	"github.com/thellmwhisperer/agentic-team-member/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
