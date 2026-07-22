// Package llmeval runs headless skeptic-style LLM evaluations by shelling
// out to CLI coding agents (codex, claude, gemini, minimax, agy) with a
// prompt on stdin and parsing a VERDICT: PASS/FAIL line from their output.
//
// This mirrors agent-orchestrator-ts's packages/cli/src/lib/llm-eval*.ts —
// the headless evaluation chain behind `ao skeptic verify` — ported to Go
// as a NEW code path alongside (not replacing) this daemon's existing
// interactive pane-spawn reviewer architecture (internal/review,
// internal/ports.Reviewer, internal/adapters/reviewer/*). All headless
// evaluation MUST route through Eval (chain.go) — do not hard-code binary
// paths or exec calls elsewhere.
package llmeval

import (
	"os"
	"regexp"
	"strings"
	"time"
)

// Timeout bounds a single model's headless evaluation call.
const Timeout = 300 * time.Second

// DefaultCodexModel resolves the codex model used for headless skeptic
// evaluation, overridable via AO_LLM_EVAL_CODEX_MODEL — mirrors TS's
// DEFAULT_CODEX_MODEL in llm-eval-shared.ts.
func DefaultCodexModel() string {
	if v := os.Getenv("AO_LLM_EVAL_CODEX_MODEL"); v != "" {
		return v
	}
	return "gpt-5.5"
}

// Result is the outcome of one model's headless evaluation attempt.
//
//   - ValidVerdict=true: a valid VERDICT line was obtained; Output carries it.
//   - ValidVerdict=false, Err=="": the tool is unavailable (not installed, no
//     credentials, rate-limited) — the caller should try the next model.
//   - ValidVerdict=false, Err!="": the tool ran but produced no VERDICT line,
//     or errored fatally — fail-closed, but the chain still falls through to
//     the next model (see Eval in chain.go).
type Result struct {
	ValidVerdict bool
	Output       string
	Err          string
}

var (
	reWord401 = regexp.MustCompile(`\b401\b`)
	reWord403 = regexp.MustCompile(`\b403\b`)
	reWord429 = regexp.MustCompile(`\b429\b`)
)

// IsUnavailable reports whether errMsg indicates the tool is unavailable
// (not installed, no credentials, rate-limited, quota exhausted, OAuth
// missing, or a local config error) so the fallback chain should move on to
// the next model rather than treating this as a fatal evaluation failure.
// Mirrors isUnavailable in llm-eval-shared.ts, plus Go-specific "binary not
// found" phrasing (Go's exec package reports missing binaries differently
// from Node's ENOENT).
func IsUnavailable(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "enoent") ||
		strings.Contains(lower, "executable file not found") ||
		strings.Contains(lower, "no such file or directory") ||
		reWord401.MatchString(errMsg) ||
		reWord403.MatchString(errMsg) ||
		reWord429.MatchString(errMsg) ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "error loading config") ||
		strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "please run /login") ||
		strings.Contains(lower, "usage limits") ||
		strings.Contains(lower, "rate limit reached") ||
		strings.Contains(lower, "review limit reached") {
		return true
	}
	return false
}

// IsAuthError reports whether msg indicates an auth failure that is global
// to all binaries sharing the same credential store (so the chain can skip
// them together rather than returning a misleading per-binary infra error).
// Mirrors isAuthError in llm-eval-shared.ts. Not yet consumed by Eval —
// reserved for a follow-up faithful port of the TS chain's auth-aware
// skip-ahead behavior (tracked in bead jleechan-xpz7); exposed now so verdict/error
// classification tests can exercise it independently.
func IsAuthError(msg string) bool {
	lower := strings.ToLower(msg)
	return reWord401.MatchString(msg) ||
		reWord403.MatchString(msg) ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "please run /login") ||
		strings.Contains(lower, "usage limits") ||
		strings.Contains(lower, "rate limit reached") ||
		strings.Contains(lower, "review limit reached")
}
