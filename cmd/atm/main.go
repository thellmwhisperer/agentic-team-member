// atm is the Go port of ATM (#150). The behaviour it owes is the contract in internal/e2e; each step of
// the port builds a piece and unskips the tests that name it. Until then it says so and exits 2.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "atm: the Go port is not built yet (#150); run python3 -m agentic_tdd_runner.harness_worker")
	os.Exit(2)
}
