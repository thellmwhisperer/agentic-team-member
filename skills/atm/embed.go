// Package atm holds the driving skill shipped with ATM.
package atm

import _ "embed"

// Skill is the repository's canonical ATM skill, embedded verbatim.
//
//go:embed SKILL.md
var Skill string
