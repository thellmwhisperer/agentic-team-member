// Package system holds the worker's system prompt shipped with ATM.
package system

import _ "embed"

// Prompt is SYSTEM.md, embedded verbatim: every claude, codex and pi worker starts with it.
//
//go:embed SYSTEM.md
var Prompt string
