package skeptic

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// maxLLMOutputChars caps the LLM output embedded in a verdict comment body.
// GitHub's comment body hard limit is 65536 chars; ~4000 is reserved for
// header/footer so llmOutput can safely occupy the bulk without risking a
// 422 on createComment/patchComment. Mirrors MAX_LLM_OUTPUT_CHARS in
// posting.ts.
const maxLLMOutputChars = 60_000

// SkepticVerdictBinding carries the request-id/head-sha markers embedded in
// a verdict comment so the skeptic-gate CI workflow can match it to the
// trigger that requested it. Mirrors SkepticVerdictBinding in posting.ts.
type SkepticVerdictBinding struct {
	RequestID string
	HeadSHA   string
}

// PostVerdict creates or updates the idempotent VERDICT comment on a PR and
// returns the exact body it posted. Mirrors postVerdict in posting.ts,
// including its 404/403 CREATE-fallback recovery when updating an existing
// comment fails (see IsGhNotFoundError/IsGhForbiddenError for the exact
// recoverable-vs-not classification).
//
// existingCommentID is 0 when there is no existing comment to update
// (mirrors TS's `number | null`).
func PostVerdict(
	ctx context.Context,
	owner, repo string,
	prNumber int,
	verdict string,
	existingCommentID int,
	botAuthor string,
	triggerSHA string,
	llmOutput string,
	binding *SkepticVerdictBinding,
) (string, error) {
	// Known, deliberate divergence: TS scans `llmOutput ?? verdict` — nullish
	// coalescing falls back to verdict only when llmOutput is
	// undefined/unset (not passed at all), NOT when it's an explicitly
	// empty string. Go has no unset-vs-empty-string distinction for a plain
	// string param (would need a *string to represent it), so this falls
	// back to verdict whenever llmOutput == "", which very narrowly
	// diverges from TS if a caller ever passes an explicit empty-string
	// llmOutput AND verdict itself happens to contain gate markers. In
	// practice every real caller (the not-yet-ported `ao skeptic verify`
	// CLI wiring) always passes the full raw LLM output here — llmOutput is
	// never intentionally empty when the marker source matters — so this is
	// accepted as a low-risk simplification rather than adding a *string
	// parameter to every call site for an edge case that doesn't arise in
	// practice.
	markerSource := llmOutput
	if markerSource == "" {
		markerSource = verdict
	}
	gateMarkers := ExtractSkepticGateMarkers(markerSource)

	var lines []string
	lines = append(lines, "<!-- skeptic-agent-verdict -->")
	if binding != nil && binding.RequestID != "" {
		lines = append(lines, fmt.Sprintf("<!-- skeptic-request-id-%s -->", binding.RequestID))
	}
	// Always include head-sha when provided, even if updating a comment
	// posted without it.
	if binding != nil && binding.HeadSHA != "" {
		lines = append(lines, fmt.Sprintf("<!-- skeptic-head-sha-%s -->", binding.HeadSHA))
	}
	lines = append(lines, gateMarkers...)
	lines = append(lines,
		"**🤖 Skeptic Agent Verdict (bd-qw6)**",
		"",
		verdict,
		"",
	)
	// Include capped LLM output so FAIL/SKIPPED comments carry context. When
	// llmOutput === verdict (no trailing text), this is a no-op duplicate,
	// matching TS's `llmOutput && llmOutput !== verdict` guard.
	if llmOutput != "" && llmOutput != verdict {
		truncated := llmOutput
		if len([]rune(llmOutput)) > maxLLMOutputChars {
			truncated = string([]rune(llmOutput)[:maxLLMOutputChars])
		}
		lines = append(lines, "--- Full skeptic output ---\n"+truncated)
	}
	lines = append(lines,
		"",
		fmt.Sprintf("_Posted by %s · %s_", botAuthor, time.Now().UTC().Format(time.RFC3339)),
	)
	if triggerSHA != "" {
		lines = append(lines,
			fmt.Sprintf("<!-- skeptic-gate-trigger-%s -->", triggerSHA),
			fmt.Sprintf("<!-- skeptic-cron-trigger-%s -->", triggerSHA),
		)
	} else {
		lines = append(lines, "", "")
	}
	body := strings.Join(lines, "\n")

	if existingCommentID != 0 {
		err := PatchComment(ctx, owner, repo, existingCommentID, body)
		if err != nil {
			// bd-479: 404 (comment deleted) or 403 (cross-user edit blocked,
			// e.g. the verdict comment was posted by a different GitHub
			// account than the current `gh` auth) both fall back to CREATE —
			// this loses idempotent re-use but guarantees the verdict lands
			// on the PR, which is what Skeptic Gate polls for. Every other
			// error (auth failure on the caller's own account, rate-limit,
			// network, a 422 from an oversized body) is rethrown so the
			// caller can handle retries/failures instead of silently
			// creating a duplicate verdict comment.
			if !IsGhNotFoundError(err) && !IsGhForbiddenError(err) {
				return "", err
			}
			if _, createErr := CreateComment(ctx, owner, repo, prNumber, body); createErr != nil {
				return "", createErr
			}
		}
	} else {
		if _, err := CreateComment(ctx, owner, repo, prNumber, body); err != nil {
			return "", err
		}
	}

	return body, nil
}

var (
	re404               = regexp.MustCompile(`(?i)\b404\b`)
	reNotFound          = regexp.MustCompile(`(?i)not\s+found`)
	re403               = regexp.MustCompile(`(?i)\b403\b`)
	reForbidden         = regexp.MustCompile(`(?i)forbidden`)
	reRateLimitMsg      = regexp.MustCompile(`(?i)rate\s*limit`)
	reAbuse             = regexp.MustCompile(`(?i)abuse`)
	reAuthentication    = regexp.MustCompile(`(?i)authentication`)
	reInvalidToken      = regexp.MustCompile(`(?i)invalid\s+token`)
	reResourceNotAccess = regexp.MustCompile(`(?i)resource\s+not\s+accessible`)
	reEditConflict      = regexp.MustCompile(`(?i)cannot\s+edit|not\s+the\s+author|only\s+the\s+creator|edit\s+conflict|must\s+be\s+the\s+author|must\s+be\s+the\s+repository`)
)

// IsGhNotFoundError reports whether err indicates a GitHub API 404 / "not
// found" response. Mirrors isGhNotFoundError in posting.ts.
func IsGhNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return re404.MatchString(msg) || reNotFound.MatchString(msg)
}

// IsGhForbiddenError reports whether err indicates a GitHub API 403 /
// "forbidden" response that specifically represents a recoverable
// cross-user comment-edit conflict (as opposed to auth failure, rate
// limiting, or abuse detection, which are NOT recoverable by falling back
// to CREATE). Mirrors isGhForbiddenError in posting.ts exactly, including
// its three-stage classification: (1) must look like a 403/forbidden at
// all, (2) must NOT also look like a non-recoverable auth/rate-limit/abuse
// failure, (3) must specifically mention an edit-authority conflict.
func IsGhForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()

	is403 := re403.MatchString(msg)
	isForbidden := reForbidden.MatchString(msg)
	if !is403 && !isForbidden {
		return false
	}

	isNonRecoverable := reRateLimitMsg.MatchString(msg) ||
		reAbuse.MatchString(msg) ||
		reAuthentication.MatchString(msg) ||
		reInvalidToken.MatchString(msg) ||
		reResourceNotAccess.MatchString(msg)
	if isNonRecoverable {
		return false
	}

	return reEditConflict.MatchString(msg)
}
