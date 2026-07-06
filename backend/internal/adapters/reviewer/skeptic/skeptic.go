// Package skeptic adapts the TS AO Skeptic worker as a Go reviewer.
//
// Skeptic is a one-shot CLI (not a prompt-driven interactive agent), so this
// adapter is a thin shell: ReviewCommand returns the argv AO should run to
// invoke `ao-ts skeptic verify` over the worker's checkout. The CLI itself
// fetches the PR diff, runs the LLM eval (Codex/Claude/Gemini fallback chain
// in llm-eval-shared.ts), and posts the verdict as a PR comment. The daemon
// records the review via `ao review submit` once the CLI exits.
//
// Per the fork's Path D plan (project_2026-06-25_path_d_confirmed.md), Skeptic
// is one of the two named TS islands retained under Go: this adapter is the
// bridge that lets Go drive the existing TS implementation without rewriting
// it in Go.
package skeptic

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the skeptic code-review adapter. It owns no state — every method
// is a pure function of the ReviewInvocation — so a single instance is safe
// to share across concurrent Spawn/Notify calls.
type Reviewer struct{}

// New builds the skeptic reviewer adapter.
func New() *Reviewer {
	return &Reviewer{}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerSkeptic
}

var _ ports.Reviewer = (*Reviewer)(nil)

// prURLRe matches the two PR URL shapes GitHub issues: a plain
// https://github.com/<owner>/<repo>/pull/<n> URL with no query/fragment.
var prURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)(?:[/?#].*)?$`)

// parsePRURL extracts (owner, repo, number) from a GitHub PR URL. Returns an
// error if the URL does not match the expected shape; the caller should treat
// that as a misconfigured invocation rather than a fallback case.
func parsePRURL(raw string) (owner, repo string, num int, err error) {
	m := prURLRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", 0, fmt.Errorf("not a github pull request URL: %q", raw)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("pull request number %q: %w", m[3], err)
	}
	return m[1], m[2], n, nil
}

// requestIDForRun returns the deterministic request id the daemon should hand
// to the Skeptic CLI for this pass. Format: gate-<sha>-<run-id>-1. The
// numeric suffix is hardcoded to 1 because the daemon treats each Spawn as a
// fresh request; if the same run is re-triggered via Notify the suffix stays
// stable so the GHA Skeptic Gate poll matches the verdict.
func requestIDForRun(inv ports.ReviewInvocation) string {
	return fmt.Sprintf("gate-%s-%s-1", inv.TargetSHA, inv.RunID)
}

// ReviewCommand builds the argv AO should exec to run the TS Skeptic worker
// over the worker's checkout. The CLI exits with the verdict embedded in its
// stdout/stderr; the daemon treats any non-zero exit as a review failure and
// re-attempts on the next Notify.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	owner, repo, num, err := parsePRURL(inv.PRURL)
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("skeptic reviewer: %w", err)
	}
	if inv.TargetSHA == "" {
		return ports.ReviewCommandSpec{}, fmt.Errorf("skeptic reviewer: target SHA is required")
	}
	if inv.RunID == "" {
		return ports.ReviewCommandSpec{}, fmt.Errorf("skeptic reviewer: run id is required")
	}

	argv := []string{
		"ao-ts",
		"skeptic",
		"verify",
		"--pr", strconv.Itoa(num),
		"--repo", owner + "/" + repo,
		"--trigger-sha", inv.TargetSHA,
		"--request-id", requestIDForRun(inv),
	}

	return ports.ReviewCommandSpec{Argv: argv}, nil
}

// ReviewMessage returns the text AO injects into an already-running skeptic
// pane to ask it to review a new commit. Skeptic is one-shot, so a live
// reviewer pane is unusual; we forward the centrally-authored prompt so a
// Notify on a held-open pane still receives coherent instructions.
func (r *Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	owner, repo, num, err := parsePRURL(inv.PRURL)
	if err != nil {
		return "", fmt.Errorf("skeptic reviewer: %w", err)
	}
	target := inv.TargetSHA
	if target == "" {
		target = "<unknown head sha>"
	}
	rid := inv.RunID
	if rid == "" {
		rid = "<unknown run id>"
	}
	var b strings.Builder
	b.WriteString("Re-run Skeptic reviewer for ")
	b.WriteString(owner)
	b.WriteByte('/')
	b.WriteString(repo)
	b.WriteString(" PR #")
	b.WriteString(strconv.Itoa(num))
	b.WriteString(" (head ")
	b.WriteString(target)
	b.WriteString(", run ")
	b.WriteString(rid)
	b.WriteString(").\n\n")
	b.WriteString("Run: ao-ts skeptic verify --pr ")
	b.WriteString(strconv.Itoa(num))
	b.WriteString(" --repo ")
	b.WriteString(owner)
	b.WriteByte('/')
	b.WriteString(repo)
	b.WriteString(" --trigger-sha ")
	b.WriteString(target)
	b.WriteString(" --request-id ")
	b.WriteString(requestIDForRun(inv))
	b.WriteString("\n\n")
	b.WriteString("Then submit via `ao review submit --run-id ")
	b.WriteString(rid)
	b.WriteString(" --verdict PASS|FAIL` based on the verdict.\n")
	return b.String(), nil
}

// ensure encoding imports are referenced even if the linter would prune them.
var _ = regexp.MustCompile