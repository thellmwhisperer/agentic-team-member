// Package buildinfo is atm's version, stamped by the release workflow.
package buildinfo

import "runtime/debug"

// Set via ldflags at build time:
//
//	-ldflags "-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=v1.0.0
//	          -X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Commit=abc1234
//	          -X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Date=2024-01-01"
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// CurrentVersion is the injected version, else the module version go install stamped, else dev.
func CurrentVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return "dev"
}

// String is the version, commit and build date.
func String() string {
	return CurrentVersion() + " (" + Commit + ") " + Date
}
