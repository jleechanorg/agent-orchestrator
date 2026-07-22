package skeptic

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/skeptic/llmeval"
)

// skepticGateMarkerRe matches a single `<!-- skeptic-gate-N:PASS|FAIL|SKIPPED -->`
// marker line. Mirrors the regex literal inline in extractSkepticGateMarkers
// in verdict-utils.ts.
var skepticGateMarkerRe = regexp.MustCompile(`(?i)<!--\s*skeptic-gate-(?:[1-8]|8a|8b|8c|8d)\s*:\s*(?:PASS|FAIL|SKIPPED)\s*-->`)

// ExtractSkepticGateMarkers returns every skeptic-gate-N marker comment
// found in body, in order of occurrence. Mirrors extractSkepticGateMarkers
// in verdict-utils.ts.
func ExtractSkepticGateMarkers(body string) []string {
	return skepticGateMarkerRe.FindAllString(body, -1)
}

// EscapeRegexLiteral escapes s for safe embedding in a regex pattern.
// Mirrors escapeRegexLiteral in verdict-utils.ts; implemented via Go's
// built-in regexp.QuoteMeta rather than hand-rolling the same character
// class, since QuoteMeta already covers every RE2 metacharacter JS's
// pattern targets.
func EscapeRegexLiteral(s string) string {
	return regexp.QuoteMeta(s)
}

// VerdictLineRegex matches a VERDICT: PASS|FAIL|SKIPPED line, tolerating
// blockquote/markdown-bold/ATX-header wrapping. Mirrors VERDICT_LINE_RE
// (= buildVerdictLineRe(["PASS","FAIL","SKIPPED"])) in verdict-utils.ts.
// Reuses llmeval.LineRegex directly (the pattern-building logic that was
// briefly duplicated locally here — see git history commit bc400fe4 — is
// now consolidated into one definition now that internal/skeptic/llmeval
// exists on this branch, per the follow-up tracked in bead jleechan-xpz7).
// Distinct from llmeval.StrictVerdictRegex (PASS|FAIL only, used to
// validate raw headless-tool output before accepting it into the fallback
// chain) — this one accepts SKIPPED too, since it's used to parse LLM
// output / GitHub comment bodies where SKIPPED is a legitimate terminal
// verdict, not an infra-failure sentinel.
var VerdictLineRegex = llmeval.LineRegex([]string{"PASS", "FAIL", "SKIPPED"})

// HasSkepticRequestId reports whether body contains a
// skeptic-request-id-<requestId> marker. Mirrors hasSkepticRequestId in
// verdict-utils.ts. Always false for an empty requestId.
func HasSkepticRequestId(body, requestID string) bool {
	if requestID == "" {
		return false
	}
	re := regexp.MustCompile(`<!--\s*skeptic-request-id-` + EscapeRegexLiteral(requestID) + `\s*-->`)
	return re.MatchString(body)
}

// HasSkepticHeadSha reports whether body contains a
// skeptic-head-sha-<headSHA> marker. Mirrors hasSkepticHeadSha in
// verdict-utils.ts. Always false for an empty headSHA.
func HasSkepticHeadSha(body, headSHA string) bool {
	if headSHA == "" {
		return false
	}
	re := regexp.MustCompile(`<!--\s*skeptic-head-sha-` + EscapeRegexLiteral(headSHA) + `\s*-->`)
	return re.MatchString(body)
}

// HasCompletePassingGateMarkers reports whether body contains a PASS marker
// for every primary gate (1-8) AND every gate-8 sub-marker (8a/8b/8c/8d) —
// per the skeptic prompt's own instruction to always emit the sub-markers
// in a PASS verdict. Mirrors hasCompletePassingGateMarkers in
// verdict-utils.ts.
func HasCompletePassingGateMarkers(body string) bool {
	for gate := 1; gate <= 8; gate++ {
		re := regexp.MustCompile(fmt.Sprintf(`(?i)<!--\s*skeptic-gate-%d\s*:\s*PASS\s*-->`, gate))
		if !re.MatchString(body) {
			return false
		}
	}
	for _, sub := range []string{"8a", "8b", "8c", "8d"} {
		re := regexp.MustCompile(`(?i)<!--\s*skeptic-gate-` + sub + `\s*:\s*PASS\s*-->`)
		if !re.MatchString(body) {
			return false
		}
	}
	return true
}

// IsFreshPassVerdictContractSatisfied reports whether a PASS verdict
// comment body carries the request-id + head-sha markers matching the
// current trigger AND a complete set of passing gate markers — the
// contract a "fresh" (not stale/replayed) PASS verdict must satisfy.
// Mirrors isFreshPassVerdictContractSatisfied in verdict-utils.ts.
func IsFreshPassVerdictContractSatisfied(body, headSHA, requestID string) bool {
	return HasSkepticRequestId(body, requestID) &&
		HasSkepticHeadSha(body, headSHA) &&
		HasCompletePassingGateMarkers(body)
}

// BoundVerdictOutput is the result of parsing a VERDICT line out of raw LLM
// output, with a fail-closed downgrade applied when a claimed PASS lacks a
// complete gate-marker table. Mirrors BoundVerdictOutput in
// verdict-utils.ts.
type BoundVerdictOutput struct {
	VerdictLine string
	LLMOutput   string
	// VerdictType is "PASS", "FAIL", "SKIPPED", or "" when no VERDICT line
	// could be parsed at all (mirrors TS's `Verdict | null`).
	VerdictType string
}

// BindVerdictOutput parses the terminal VERDICT line from raw llmOutput. If
// no VERDICT line is found, it fails closed with a synthesized FAIL line
// appended to the output. If the parsed verdict is PASS but the gate-marker
// table is incomplete, it downgrades to FAIL (a fail-closed safety net
// against a model claiming PASS without emitting its required evidence
// markers). Mirrors bindVerdictOutput in verdict-utils.ts.
func BindVerdictOutput(llmOutput string) BoundVerdictOutput {
	m := VerdictLineRegex.FindStringSubmatch(llmOutput)
	if m == nil {
		verdictLine := "VERDICT: FAIL — could not parse LLM output (expected VERDICT: PASS/FAIL/SKIPPED)"
		return BoundVerdictOutput{
			VerdictLine: verdictLine,
			LLMOutput:   llmOutput + "\n\n" + verdictLine,
			VerdictType: "",
		}
	}

	verdictType := strings.ToUpper(m[1])
	downgradedPass := verdictType == "PASS" && !HasCompletePassingGateMarkers(llmOutput)
	if downgradedPass {
		verdictLine := "VERDICT: FAIL — PASS missing complete skeptic gate table"
		return BoundVerdictOutput{
			VerdictLine: verdictLine,
			LLMOutput:   llmOutput + "\n\n" + verdictLine,
			VerdictType: "FAIL",
		}
	}
	return BoundVerdictOutput{
		VerdictLine: m[0],
		LLMOutput:   llmOutput,
		VerdictType: verdictType,
	}
}

// GetVerdictColor maps a verdict type to a display color name. Mirrors
// getVerdictColor in verdict-utils.ts.
func GetVerdictColor(verdictType string) string {
	switch verdictType {
	case "PASS":
		return "green"
	case "SKIPPED":
		return "yellow"
	default:
		return "red"
	}
}
