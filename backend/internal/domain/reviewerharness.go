package domain

// ReviewerHarness identifies a code-review agent. It is a separate vocabulary
// from AgentHarness on purpose: a reviewer-only tool (e.g. the Greptile CLI)
// must not become a valid worker, and a worker harness does not automatically
// become a valid reviewer. The two sets are maintained independently and only
// happen to share ids where the same tool serves both roles.
type ReviewerHarness string

// Supported reviewer harnesses. Add a reviewer-only tool here (and register its
// adapter) without widening the worker AgentHarness set.
const (
	ReviewerClaudeCode ReviewerHarness = "claude-code"
	ReviewerCodex      ReviewerHarness = "codex"
	ReviewerOpenCode   ReviewerHarness = "opencode"
	// ReviewerSkeptic is the AO Skeptic worker (TS island) reached via the
	// `ao-ts` CLI. See internal/adapters/reviewer/skeptic. Per Path D
	// (project_2026-06-25_path_d_confirmed.md) Skeptic is one of the two TS
	// islands retained under Go; the rest of the worker vocabulary is Go.
	ReviewerSkeptic ReviewerHarness = "skeptic"
)

// AllReviewerHarnesses is the canonical set used to validate a configured
// reviewer harness.
var AllReviewerHarnesses = []ReviewerHarness{
	ReviewerClaudeCode,
	ReviewerCodex,
	ReviewerOpenCode,
	ReviewerSkeptic,
}

// IsKnown reports whether h is one of the supported reviewer harnesses.
func (h ReviewerHarness) IsKnown() bool {
	for _, k := range AllReviewerHarnesses {
		if h == k {
			return true
		}
	}
	return false
}
