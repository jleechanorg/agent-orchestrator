// Package skeptic ports agent-orchestrator-ts's headless `ao skeptic verify`
// pipeline (packages/cli/src/commands/skeptic/) to Go: prompt construction,
// GH comment posting, and merge-gate state fetching, built on top of the
// internal/skeptic/llmeval evaluation chain. This is a NEW code path
// alongside (not replacing) the daemon's existing interactive pane-spawn
// reviewer architecture (internal/review, internal/ports.Reviewer,
// internal/adapters/reviewer/*).
package skeptic

// PRInfo is the subset of GitHub PR metadata the skeptic prompt needs.
// Mirrors PRInfo in gh-client.ts.
type PRInfo struct {
	Number      int
	Title       string
	Body        string
	State       string
	HeadRefOID  string
	BaseRefName string
	IsDraft     bool
}

// ReviewAuthor is a review's author login, mirroring ReviewInfo.author's
// `{ login: string } | null` shape in gh-client.ts.
type ReviewAuthor struct {
	Login string
}

// ReviewInfo is one PR review (e.g. a CodeRabbit pass), mirroring
// ReviewInfo in gh-client.ts.
type ReviewInfo struct {
	Author *ReviewAuthor
	// State is one of "approved", "changes_requested", "commented",
	// "dismissed", "pending".
	State string
	Body  string
	// SubmittedAt is an RFC3339-ish timestamp string, kept as a string
	// (rather than time.Time) to mirror the TS source's string handling —
	// callers that need to sort by it fall back to a zero time on parse
	// failure, exactly like TS's `new Date(x).getTime() || 0`.
	SubmittedAt string
	// CommitID is the commit OID this review is attached to (populated
	// from GraphQL commit.oid); empty when unknown. Used to filter stale
	// reviews submitted on a superseded head SHA.
	CommitID string
}

// IsCodeRabbitReview reports whether r was authored by the CodeRabbit bot.
// Mirrors isCodeRabbitReview in gh-client.ts. GraphQL's author.login
// returns "coderabbitai" (no [bot] suffix); some paths report
// "coderabbitai[bot]" — both are accepted.
func IsCodeRabbitReview(r ReviewInfo) bool {
	if r.Author == nil {
		return false
	}
	return r.Author.Login == "coderabbitai" || r.Author.Login == "coderabbitai[bot]"
}

// CheckRunSummary is one CI check run's result. Mirrors CheckRunSummary in
// mergeGate.ts.
type CheckRunSummary struct {
	Name       string
	Status     string
	Conclusion string // empty string means null/not-yet-concluded
}

// MergeGateState is the aggregated PR readiness state fed into the skeptic
// prompt. Mirrors MergeGateState in mergeGate.ts. Populating this from the
// real GitHub API (fetchMergeGateState) is a separate, not-yet-ported piece
// (bead jleechan-xpz7) — this type exists now so BuildSkepticPrompt has a
// stable signature to build and test against.
type MergeGateState struct {
	CIPassing bool
	// CIRawState is the raw commit status state from the GitHub API (e.g.
	// "success", "failure", "pending").
	CIRawState  string
	CheckRuns   []CheckRunSummary
	NoConflicts bool
	// MergeableRaw mirrors the raw mergeable tri-state from the GitHub API:
	// nil = not yet determined, else true=MERGEABLE / false=CONFLICTING.
	MergeableRaw               *bool
	CRApproved                 bool
	CRState                    string
	CRDismissedWithoutApproval bool
	BugbotErrors               int
	UnresolvedBlockingComments int
	EvidenceApproved           bool
	EvidenceRequired           bool
	// SkepticVerdict is one of "PASS", "FAIL", "SKIPPED", or "" (not
	// posted yet — mirrors TS's `| null`).
	SkepticVerdict   string
	SkepticCommentID int // 0 means none (mirrors TS's `| null`)
}
