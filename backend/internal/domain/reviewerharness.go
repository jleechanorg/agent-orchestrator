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
	// ReviewerShell is the generic config-driven reviewer adapter. Operators
	// wire one-off/custom harnesses (skeptic, agy wrapper, custom CLIs) via
	// ReviewerConfig.Cmd + Env with template substitution — no per-tool Go
	// adapter code required. Matches prior-art shell primitives in Jenkins sh,
	// Drone commands, GitHub composite actions, Tekton steps, Argo script. See
	// internal/adapters/reviewer/shell.
	ReviewerShell ReviewerHarness = "shell"
)

// AllReviewerHarnesses is the canonical set used to validate a configured
// reviewer harness.
var AllReviewerHarnesses = []ReviewerHarness{
	ReviewerClaudeCode,
	ReviewerCodex,
	ReviewerOpenCode,
	ReviewerShell,
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
