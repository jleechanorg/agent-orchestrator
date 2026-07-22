package skeptic

import (
	"fmt"
	"regexp"
	"strings"
)

// EvidenceResult is the outcome of a single claim-verification evidence
// layer check. Mirrors EvidenceResult in claim-verifier.ts.
type EvidenceResult string

const (
	EvidencePass   EvidenceResult = "pass"
	EvidenceFail   EvidenceResult = "fail"
	EvidenceAbsent EvidenceResult = "absent"
)

// ClaimCheck is the result of checking one evidence layer (run-level or
// comment-level). Mirrors ClaimCheck in claim-verifier.ts.
type ClaimCheck struct {
	Label  string
	Result EvidenceResult
	Detail string
}

// ClaimVerificationResult is the outcome of VerifySkepticClaim. Mirrors
// ClaimVerificationResult in claim-verifier.ts.
type ClaimVerificationResult struct {
	// Outcome is "PASS", "FAIL", or "INSUFFICIENT".
	Outcome      string
	RunLevel     ClaimCheck
	CommentLevel ClaimCheck
	Summary      string
	// BlocksWorking is true whenever Outcome != "PASS" (INSUFFICIENT always
	// blocks; FAIL also blocks).
	BlocksWorking bool
}

var skepticCommentMarkerRe = regexp.MustCompile(`(?i)<!--\s*skeptic-agent-verdict\s*-->`)

// CheckRunLevel checks the run-level (CLI/LLM output) evidence layer.
// Mirrors checkRunLevel in claim-verifier.ts.
func CheckRunLevel(llmOutput string) ClaimCheck {
	trimmed := strings.TrimSpace(llmOutput)
	if trimmed == "" {
		return ClaimCheck{
			Label:  "run-level",
			Result: EvidenceAbsent,
			Detail: "No CLI output — LLM evaluation did not run or produced no output",
		}
	}
	m := VerdictLineRegex.FindStringSubmatch(trimmed)
	if m == nil {
		preview := trimmed
		if len(preview) > 80 {
			preview = preview[:80]
		}
		return ClaimCheck{
			Label:  "run-level",
			Result: EvidenceAbsent,
			Detail: fmt.Sprintf("No VERDICT: PASS/FAIL in CLI output (got: %s...)", preview),
		}
	}
	verdict := strings.ToUpper(m[1])
	if verdict == "SKIPPED" {
		return ClaimCheck{
			Label:  "run-level",
			Result: EvidenceFail,
			Detail: fmt.Sprintf("VERDICT: FAIL — infra: SKIPPED verdict (%s)", m[0]),
		}
	}
	result := EvidenceFail
	if verdict == "PASS" {
		result = EvidencePass
	}
	return ClaimCheck{
		Label:  "run-level",
		Result: result,
		Detail: fmt.Sprintf("VERDICT: %s — %s", verdict, m[0]),
	}
}

// CheckCommentLevel checks the comment-level (GitHub PR comment) evidence
// layer. Mirrors checkCommentLevel in claim-verifier.ts.
func CheckCommentLevel(commentBody string) ClaimCheck {
	trimmed := strings.TrimSpace(commentBody)
	if trimmed == "" {
		return ClaimCheck{
			Label:  "comment-level",
			Result: EvidenceAbsent,
			Detail: "No <!-- skeptic-agent-verdict --> comment found on the PR",
		}
	}
	if !skepticCommentMarkerRe.MatchString(trimmed) {
		return ClaimCheck{
			Label:  "comment-level",
			Result: EvidenceAbsent,
			Detail: "Comment exists but lacks <!-- skeptic-agent-verdict --> marker",
		}
	}
	m := VerdictLineRegex.FindStringSubmatch(trimmed)
	if m == nil {
		return ClaimCheck{
			Label:  "comment-level",
			Result: EvidenceAbsent,
			Detail: "Comment lacks VERDICT: PASS/FAIL line",
		}
	}
	verdict := strings.ToUpper(m[1])
	if verdict == "SKIPPED" {
		return ClaimCheck{
			Label:  "comment-level",
			Result: EvidenceFail,
			Detail: "VERDICT: FAIL — infra: SKIPPED verdict in comment",
		}
	}
	result := EvidenceFail
	if verdict == "PASS" {
		result = EvidencePass
	}
	return ClaimCheck{
		Label:  "comment-level",
		Result: result,
		Detail: fmt.Sprintf("VERDICT: %s in PR comment", verdict),
	}
}

// VerifySkepticClaim cross-checks run-level (llmOutput) and comment-level
// (commentBody) evidence. Fail-closed: PASS only when both layers pass,
// FAIL when either layer fails, INSUFFICIENT for any other combination
// (missing, ambiguous, or inconsistent). Mirrors verifySkepticClaim in
// claim-verifier.ts.
func VerifySkepticClaim(llmOutput, commentBody string) ClaimVerificationResult {
	runLevel := CheckRunLevel(llmOutput)
	commentLevel := CheckCommentLevel(commentBody)

	var outcome, summary string
	switch {
	case runLevel.Result == EvidencePass && commentLevel.Result == EvidencePass:
		outcome = "PASS"
		summary = "Both run-level and comment-level VERDICT: PASS — claim verified."
	case runLevel.Result == EvidenceFail || commentLevel.Result == EvidenceFail:
		outcome = "FAIL"
		failLayer := commentLevel
		if runLevel.Result == EvidenceFail {
			failLayer = runLevel
		}
		summary = fmt.Sprintf("FAIL verdict in %s layer — claim contradicted: %s", failLayer.Label, failLayer.Detail)
	default:
		outcome = "INSUFFICIENT"
		var reasons []string
		if runLevel.Result == EvidenceAbsent {
			reasons = append(reasons, runLevel.Detail)
		}
		if commentLevel.Result == EvidenceAbsent {
			reasons = append(reasons, commentLevel.Detail)
		}
		summary = fmt.Sprintf("Insufficient evidence — cannot confirm PASS:\n  • %s", strings.Join(reasons, "\n  • "))
	}

	return ClaimVerificationResult{
		Outcome:       outcome,
		RunLevel:      runLevel,
		CommentLevel:  commentLevel,
		Summary:       summary,
		BlocksWorking: outcome != "PASS",
	}
}

// FormatClaimVerification renders a ClaimVerificationResult for terminal
// output. Mirrors formatClaimVerification in claim-verifier.ts (ASCII box
// characters and unicode ⚠/✓ glyphs preserved verbatim).
func FormatClaimVerification(result ClaimVerificationResult) string {
	var lines []string
	lines = append(lines, "")
	lines = append(lines, "┌─ Claim Verification ──────────────────────────────────────────────")
	lines = append(lines, fmt.Sprintf("│ Run-level:     [%-8s] %s", strings.ToUpper(string(result.RunLevel.Result)), result.RunLevel.Detail))
	lines = append(lines, fmt.Sprintf("│ Comment-level: [%-8s] %s", strings.ToUpper(string(result.CommentLevel.Result)), result.CommentLevel.Detail))
	lines = append(lines, "├──────────────────────────────────────────────────────────────────")
	firstSummaryLine := strings.SplitN(result.Summary, "\n", 2)[0]
	lines = append(lines, fmt.Sprintf("│ Outcome:       [%-8s] %s", result.Outcome, firstSummaryLine))
	if result.Outcome != "PASS" {
		lines = append(lines, "│ ⚠  blocks 'working' status report")
	} else {
		lines = append(lines, "│ ✓  claim verified — 'working' status permitted")
	}
	lines = append(lines, "└──────────────────────────────────────────────────────────────────")
	return strings.Join(lines, "\n")
}
