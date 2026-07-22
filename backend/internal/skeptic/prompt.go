package skeptic

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Truncation limits for content included in the skeptic prompt. Mirrors the
// MAX_* constants in prompt.ts.
const (
	maxDesignDocChars     = 15_000
	maxPRDescriptionChars = 100_000
	maxDiffChars          = 500_000
	maxReviewBodyChars    = 200
	maxReviewsToShow      = 8
	maxTotalTestFileChars = 20_000
	maxTestFileChars      = 10_000
)

// fabricatedPatterns are regexes indicating fabricated/placeholder evidence
// in PR descriptions. Mirrors FABRICATED_PATTERNS in prompt.ts.
var fabricatedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)simulated`),
	regexp.MustCompile(`(?i)example\.com`),
	regexp.MustCompile(`(?i)<screenshot[^>]*>`),
	regexp.MustCompile(`(?i)<value>`),
	regexp.MustCompile(`(?i)\bTODO\b`),
	regexp.MustCompile(`(?i)\bTBD\b`),
	regexp.MustCompile(`(?i)placeholder`),
}

var (
	evidenceHeadingRe = regexp.MustCompile(`(?im)^##\s*Evidence`)
	nextHeadingRe     = regexp.MustCompile(`(?m)^##\s+`)
	coverageWordRe    = regexp.MustCompile(`(?i)\bcoverage\b`)
	coveragePercentRe = regexp.MustCompile(`\d\s*%`)
)

// IsEvidenceAuthentic is a deterministic check: does the PR body's
// ## Evidence section avoid fabricated/placeholder patterns and unsupported
// coverage claims? Mirrors isEvidenceAuthentic in prompt.ts exactly,
// including Rule 10's "empty Evidence section is a FAIL, never default to
// authentic" behavior.
func IsEvidenceAuthentic(body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	// Scope to the ## Evidence section only — avoid false FAILs from
	// TODO/TBD in other sections.
	loc := evidenceHeadingRe.FindStringIndex(body)
	if loc == nil {
		return false
	}
	afterHeading := body[loc[1]:]
	// Cut the heading's own line off (everything up to and including the
	// first newline), matching TS's split on the heading regex which
	// discards the matched heading text itself.
	if idx := strings.IndexByte(afterHeading, '\n'); idx != -1 {
		afterHeading = afterHeading[idx+1:]
	} else {
		afterHeading = ""
	}
	evidenceContent := afterHeading
	if next := nextHeadingRe.FindStringIndex(afterHeading); next != nil {
		evidenceContent = afterHeading[:next[0]]
	}
	if strings.TrimSpace(evidenceContent) == "" {
		return false
	}
	for _, pattern := range fabricatedPatterns {
		if pattern.MatchString(evidenceContent) {
			return false
		}
	}
	// Coverage claim without a percentage — scan each non-command line
	// individually. Only lines starting with "$" (command invocations) are
	// exempt.
	for _, line := range strings.Split(evidenceContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "$") {
			continue
		}
		if coverageWordRe.MatchString(trimmed) && !coveragePercentRe.MatchString(trimmed) {
			return false
		}
	}
	return true
}

var (
	diffGitHeaderRe = regexp.MustCompile(`^diff --git [ab]/(.+?) [ab]/(.+)$`)
	diffOldPathRe   = regexp.MustCompile(`^---[ \t]a/(.+)$`)
	diffBinaryRe    = regexp.MustCompile(`^Binary files [ab]/(.+) and [ab]/(.+) differ$`)
)

// getChangedFiles extracts file paths from a unified diff string. Mirrors
// getChangedFiles in prompt.ts (deletion detection via `+++ /dev/null`
// pairing with the preceding `--- a/<path>` line included).
func getChangedFiles(diff string) []string {
	seen := map[string]bool{}
	var files []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	var lastOldPath string
	for _, line := range strings.Split(diff, "\n") {
		if m := diffGitHeaderRe.FindStringSubmatch(line); m != nil {
			add(m[2])
			lastOldPath = ""
			continue
		}
		if m := diffOldPathRe.FindStringSubmatch(line); m != nil {
			lastOldPath = m[1]
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			if strings.HasPrefix(line, "+++ /dev/null") && lastOldPath != "" {
				add(lastOldPath)
			}
			lastOldPath = ""
			continue
		}
		if m := diffBinaryRe.FindStringSubmatch(line); m != nil {
			add(m[2])
			lastOldPath = ""
		}
	}
	return files
}

// sortReviewsNewestFirst sorts reviews newest-first, tolerating missing/
// unparseable timestamps by treating them as zero (mirrors
// `new Date(x).getTime() || 0` in prompt.ts/mergeGate.ts).
func sortReviewsNewestFirst(reviews []ReviewInfo) []ReviewInfo {
	out := make([]ReviewInfo, len(reviews))
	copy(out, reviews)
	sort.SliceStable(out, func(i, j int) bool {
		return reviewTimeMillis(out[i].SubmittedAt) > reviewTimeMillis(out[j].SubmittedAt)
	})
	return out
}

func reviewTimeMillis(s string) int64 {
	t, err := parseRFC3339Lenient(s)
	if err != nil {
		return 0
	}
	return t
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// BuildSkepticPrompt constructs the skeptical LLM evaluation prompt from PR
// state, diff, reviews, an optional design doc, and optional test file
// contents. Mirrors buildSkepticPrompt in prompt.ts — the prompt TEXT
// (rules, output format, examples) is transcribed verbatim since small
// wording changes can materially change evaluation quality; only the
// surrounding Go plumbing (string building, sorting, truncation) differs in
// form from the TS array-join pattern.
func BuildSkepticPrompt(pr PRInfo, state MergeGateState, diff string, reviews []ReviewInfo, designDoc *string, testFiles []TestFileContent) string {
	// If CodeRabbit approved via comment fallback, prepend a virtual
	// APPROVED review so the LLM isn't blocked by dismissed reviews (Rule 4).
	hasApprovedReview := false
	for _, r := range reviews {
		if IsCodeRabbitReview(r) && strings.ToLower(r.State) == "approved" {
			hasApprovedReview = true
			break
		}
	}
	modifiedReviews := make([]ReviewInfo, len(reviews))
	copy(modifiedReviews, reviews)
	if state.CRApproved && !hasApprovedReview {
		modifiedReviews = append(modifiedReviews, ReviewInfo{
			Author:      &ReviewAuthor{Login: "coderabbitai"},
			State:       "approved",
			Body:        "Approving via comment fallback [approve]",
			SubmittedAt: nowRFC3339(),
		})
	}

	crDetail := state.CRState
	if state.CRDismissedWithoutApproval {
		crDetail = state.CRState + " + DISMISSED_WITHOUT_APPROVAL"
	}

	// Detect CR APPROVED with body_len=0 explicitly so the model cannot
	// miss it.
	var latestCRDecisive *ReviewInfo
	{
		var decisive []ReviewInfo
		for _, r := range modifiedReviews {
			st := strings.ToLower(r.State)
			if IsCodeRabbitReview(r) && (st == "approved" || st == "changes_requested") && pr.HeadRefOID != "" && r.CommitID == pr.HeadRefOID {
				decisive = append(decisive, r)
			}
		}
		sorted := sortReviewsNewestFirst(decisive)
		if len(sorted) > 0 {
			latestCRDecisive = &sorted[0]
		}
	}
	crEmptyBodyApproved := latestCRDecisive != nil &&
		strings.ToLower(latestCRDecisive.State) == "approved" &&
		len(latestCRDecisive.Body) == 0

	unresolvedLabel := "PASS"
	if state.UnresolvedBlockingComments > 0 {
		unresolvedLabel = fmt.Sprintf("FAIL (%d blocking)", state.UnresolvedBlockingComments)
	}

	evidenceLabel := "(see Rule 10)"
	if state.EvidenceRequired {
		if state.EvidenceApproved {
			evidenceLabel = "PASS"
		} else {
			evidenceLabel = "FAIL"
		}
	}

	var designDocSection string
	if designDoc != nil {
		designDocSection = strings.Join([]string{
			"",
			fmt.Sprintf("--- DESIGN DOC (docs/design/pr-designs/pr-%d.md) ---", pr.Number),
			truncateRunes(*designDoc, maxDesignDocChars),
		}, "\n")
	} else {
		designDocSection = strings.Join([]string{
			"",
			"--- DESIGN DOC ---",
			"DESIGN DOC NOT FOUND for this PR. The generate-pr-design-docs.yml workflow",
			"should have generated one on PR open. If no design doc exists AND the PR body",
			"does not explicitly claim 'DESIGN DOC: N/A' with a justification, flag this as a gap.",
		}, "\n")
	}

	var prDescriptionSection string
	if pr.Body != "" {
		prDescriptionSection = strings.Join([]string{
			"",
			"--- PR DESCRIPTION ---",
			truncateRunes(pr.Body, maxPRDescriptionChars),
		}, "\n")
	}

	headSHA := pr.HeadRefOID
	if headSHA == "" {
		headSHA = "(unknown)"
	}
	crEmptyBodySuffix := ""
	if crEmptyBodyApproved {
		crEmptyBodySuffix = " [EMPTY BODY APPROVED — valid per Rule 2]"
	}

	changedFiles := getChangedFiles(diff)
	changedFileLines := make([]string, len(changedFiles))
	for i, f := range changedFiles {
		changedFileLines[i] = "- " + f
	}

	reviewLines := reviewSummaryLines(modifiedReviews, pr.HeadRefOID)

	diffSlice := truncateRunes(diff, maxDiffChars)
	diffTruncatedNote := ""
	if len([]rune(diff)) > maxDiffChars {
		diffTruncatedNote = "\n\n[DIFF TRUNCATED - TOO LARGE]"
	}

	summaryLines := []string{
		fmt.Sprintf("PR #%d: %s", pr.Number, pr.Title),
		fmt.Sprintf("State: %s | Draft: %s", pr.State, strconv.FormatBool(pr.IsDraft)),
		fmt.Sprintf("Base: %s", pr.BaseRefName),
		fmt.Sprintf("Head SHA: %s", headSHA),
		"",
		"--- 8-GATE INPUT STATUS ---",
		fmt.Sprintf("  1. CI green:            %s", passFail(state.CIPassing)),
		fmt.Sprintf("  2. No merge conflicts:  %s", passFail(state.NoConflicts)),
		fmt.Sprintf("  3. CR APPROVED:         %s (state: %s)%s", passFail(state.CRApproved), crDetail, crEmptyBodySuffix),
		fmt.Sprintf("  4. Bugbot clean:        %s (errors: %d)", passFail(state.BugbotErrors == 0), state.BugbotErrors),
		fmt.Sprintf("  5. Comments resolved:   %s (nitpick comments excluded, per checkMergeGate)", unresolvedLabel),
		fmt.Sprintf("  6. Evidence review:    %s", evidenceLabel),
		fmt.Sprintf("  7. Prior skeptic verdict: %s", verdictOrNotPosted(state.SkepticVerdict)),
		"  8. Description/code/evidence alignment: YOU MUST EVALUATE THIS",
		"",
		"--- RECENT REVIEWS (chronological, most recent shown) ---",
	}
	summaryLines = append(summaryLines, reviewLines...)
	summaryLines = append(summaryLines,
		"",
		fmt.Sprintf("--- ALL CHANGED FILES IN PR (%d files) ---", len(changedFiles)),
		strings.Join(changedFileLines, "\n"),
		"",
		fmt.Sprintf("--- DIFF (first %d chars; all files included if diff fits) ---", maxDiffChars),
		diffSlice+diffTruncatedNote,
	)
	summary := strings.Join(summaryLines, "\n")

	testFilesSection := buildTestFilesSection(testFiles)

	body := []string{
		"You are a Skeptic QA Agent. Your job is to FIND GAPS in this PR.",
		"INVERTED INCENTIVE: You are rewarded for finding missing evidence.",
		"A false PASS is YOUR failure. A thorough FAIL report is success.",
		"",
		"RULES:",
		"1. Verify each mechanical gate independently, then perform Gate 7 technical review and Gate 8 alignment review — do not trust the status summary alone. IMPORTANT: The 'Skeptic Gate' GHA check is a self-referential poller that waits for THIS verdict — ignore its pass/fail state when evaluating Gate 1 (CI). A failing Skeptic Gate only means this verdict hasn't been posted yet, not that CI is broken.",
		"2. CR APPROVED means review state=APPROVED with body_len>0, OR body_len=0 with CR posting 'all good'/'✅'/'No actionable comments' AFTER the APPROVED. IMPORTANT: An APPROVED review with an empty body (body_len=0) is still a valid APPROVED — the empty body does NOT invalidate the approval state. CR state is already filtered by HEAD SHA above — if the summary says `state: none-on-head`, the current head has no decisive CR review and the gate should be evaluated on the absence of an on-head blocker, NOT on GitHub's UI-level reviewDecision (which can include stale reviews on superseded SHAs).",
		"3. CR COMMENTED is NOT approval. CR CHANGES_REQUESTED is NOT approval. CR CHANGES_REQUESTED on a NON-current head SHA is stale and must be ignored — only on-head reviews are decisive.",
		"4. A dismissed CR review without a subsequent real APPROVED review is a blocker.",
		"5. Bugbot errors always block merge.",
		"6. Unresolved Major/Critical inline comments always block merge (nitpicks excluded).",
		"7. If CI is still in_progress at merge time, that is a gap even if the PR is already merged.",
		"8. If CR posted CHANGES_REQUESTED and it was never addressed, that is a gap even if CR later APPROVED.",
		"9. If CR's most recent state is CHANGES_REQUESTED and the review body says 'REQUEST CHANGES', that is a gap.",
		"10. ALWAYS evaluate evidence authenticity in the PR body's ## Evidence section:",
		"    - FAIL if evidence contains 'simulated', 'example.com', '<screenshot path>', '<value>', 'TODO', 'TBD'",
		"    - FAIL if evidence mentions a coverage metric without stating the actual percentage (e.g. 'coverage is 97%' is fine; 'coverage increased' without a number is a FAIL; 'pnpm test --coverage' as a command invocation is not a coverage claim and is exempt)",
		"    - FAIL if the evidence section is empty or contains only template placeholders",
		"    - FAIL if evidence for a fix or feature does not show the TDD Red-Green cycle (must show the initial failure logs/media followed by passing ones, per repo skill: skills/tdd-evidence-workflow/SKILL.md).",
		"    - FAIL if Terminal media is provided but the URL is not HTTPS or does not point to a .mp4, .gif, .webm, .mov, or .cast file (must be video evidence per repo skill: skills/tmux-video-evidence/SKILL.md).",
		"    - FAIL if UI media is provided but the URL is not HTTPS or does not point to a .mp4, .gif, .webm, or .mov file (screenshots alone are insufficient; .cast is terminal-only, not valid for UI media; per repo skill: skills/ui-video-evidence/SKILL.md).",
		"    - FAIL if media is provided but lacks a caption or description tying it to the commit SHA.",
		"    - PASS if evidence shows real command output, real test results, or real authentic video evidence showing the TDD cycle with HTTPS non-placeholder URLs and required caption/SHA linkage as specified in skills/evidence-standards/SKILL.md.",
		"    - NOTE: Gate 6 (evidence-review-bot) is a separate pass/fail gate from your authenticity check. Gate 6 may be skipped depending on project configuration.",
		"",
		"--- DESIGN ALIGNMENT CHECK (Rule 11) ---",
		"IMPORTANT: The design doc is auto-generated from this PR's metadata and diff",
		"(via generate-pr-design-docs.yml). It is NOT an independent specification.",
		"Your job is to use it as a starting point — verify its accuracy against the diff",
		"and PR description, and flag any discrepancies or omissions.",
		"",
		"Gate 7 and Gate 8 are distinct:",
		"  - Gate 7 is your independent technical risk review of behavior, tests, and merge readiness.",
		"  - Gate 8 is product/document alignment: PR description goals, scope, tenets, diff, and evidence must agree.",
		"",
		"You MUST check that the code diff aligns with what the design doc and PR description claim.",
		"Specifically check:",
		"  11a. If the design doc says 'adds X package/file', verify the diff adds it.",
		"  11b. If the PR description says 'fixes Y bug', verify the diff contains the fix.",
		"  11c. If the design doc describes a new capability, verify the diff implements it.",
		"  11d. If the diff changes a file that the design doc/PR description does not mention, flag it as unexplained.",
		"  11e. If functionality described in the design doc is absent from the diff, flag it as missing.",
		"Gap examples that Rule 11 catches:",
		"  - Design doc says 'adds agent-foo plugin' but no new plugin file exists in the diff",
		"  - PR description says 'fixes auth token expiry bug' but no auth-related code is changed",
		"  - Design doc shows a new API endpoint but the diff only touches tests",
		"When flagging Rule 11 gaps, quote the specific design doc claim and the corresponding diff gap.",
		"SKIP Rule 11f for N/A cases: If the PR body contains 'DESIGN DOC: N/A' or 'Design doc: N/A' with a justification (e.g., 'additive skeptic prompt extension, no new architecture, pattern follows established precedent'), do NOT flag a missing design doc gap. Rules 11a-11e comparisons against the design doc are also WAIVED — there is no design doc to compare against; verify the diff aligns with the PR description claims directly instead.",
		"IMPORTANT — N/A vs NOT FOUND alignment: The design-doc section in the prompt context shows 'DESIGN DOC NOT FOUND' when no design-doc file exists (auto-generated placeholder). If the PR body explicitly claims 'DESIGN DOC: N/A' with a justification, this is CONSISTENT, not contradictory — the author is explicitly stating the design doc was not found and is not required. Do NOT flag this as a misalignment.",
		"",
		"--- GOALS PROOF (Rule 12 — enhanced) ---",
		"12. GOALS PROOF — systematic per-goal check with prove-by-tests requirement:",
		"    If the PR description contains a '## Goals' or '## Goal' section:",
		"    12a. Extract each bullet/numbered item from the Goals section",
		"    12b. Classify each goal type:",
		"        BEHAVIORAL: makes a functional claim ('adds X', 'fixes Y', 'enables Z')",
		"        STRUCTURAL: adds files, packages, dependencies, config",
		"        DOCUMENTATION: only affects docs, READMEs, comments",
		"        TESTING: explicitly about adding/updating tests",
		"    12c. For BEHAVIORAL goals:",
		"        - Find the test(s) that validate this behavior (read test files in the diff)",
		"        - Check CI output or evidence for test execution proving the behavior works",
		"        - FAIL if behavioral goal has no corresponding passing test",
		"        - FAIL if test exists but evidence doesn't show it running/passing",
		"        - The test must specifically prove the claimed behavior, not just exist",
		"    12d. For STRUCTURAL goals:",
		"        - Verify the diff adds the file/package/config",
		"        - Verify the addition is connected to the claimed goal (not orphaned)",
		"    12e. For DOCUMENTATION goals:",
		"        - Test changes not required if goal explicitly says 'docs only'",
		"    12f. For TESTING goals:",
		"        - Behavioral code changes are NOT required if goal explicitly says 'add tests'",
		"    12g. FAIL if a ## Goals section exists but no goal has any test validation",
		"    TDD Red-Green check: if the PR claims to follow TDD, verify the commit history",
		"    shows Red (failing test) before Green (passing implementation), or evidence shows",
		"    the initial failure state followed by passing state.",
		"    Credible proof forms (in order of strength):",
		"      1. CI test output showing specific test case passing with output matching the claimed behavior",
		"      2. Terminal/video evidence of running the test command locally with matching output",
		"      3. TDD Red-Green video showing initial failure, then implementation, then Green",
		"      4. Code coverage report with % showing the changed code is covered",
		"    Insufficient alone: 'tests exist', 'CI is green' (doesn't prove specific goal was tested),",
		"    'coverage increased' (doesn't prove specific goal was validated)",
		"    When flagging Rule 12 gaps, quote the specific goal bullet and explain what diff evidence you expected",
		"    NOTE: If PR has no Goals section, skip rules 12c-12g (structural/docs-only goals still checked)",
		"",
		"--- TENETS ADHERENCE CHECK (Rule 13 — NEW) ---",
		"13. TENETS ADHERENCE CHECK — for each stated principle:",
		"    Look in PR description for 'tenet', 'principle', 'should', 'must', 'rule' language.",
		"    13a. Quote the stated tenet",
		"    13b. Find the code, tests, or config that enforces it",
		"    13c. FAIL if a stated tenet has no implementing code in the diff",
		"    Example: PR says 'all auth flows must use short-lived tokens' but diff doesn't",
		"    change any auth code → FAIL tenet adherence",
		"    Example: PR says 'we follow TDD' but no Red-Green test evidence exists → FAIL",
		"    NOTE: If PR has no tenets/principles section, skip this rule",
		"",
		"--- SCOPE BOUNDARY CHECK (Rule 14 — NEW) ---",
		"14. SCOPE BOUNDARY CHECK — verify the diff stays within stated scope:",
		"    14a. Extract scope claims from the PR description (look for 'Scope', 'In Scope',",
		"        'Out of Scope', 'does X', 'adds Y', 'modifies Z' language in the description body)",
		"    14b. Identify which files and directories the PR claims to touch",
		"    14c. FAIL if the diff contains changes to files/directories that are not mentioned",
		"        in the PR description and are not explained by the Goals or Tenets sections",
		"    14d. FAIL if the PR description explicitly scopes work to X but the diff also changes Y",
		"        without any justification or connection to the scoped work",
		"    Example: PR says 'only touches packages/cli/src/commands/skeptic/' but diff modifies",
		"        packages/core/src/worker.ts → FAIL out-of-scope changes",
		"    Example: PR says 'Scope: auth middleware only' but diff adds a new file in",
		"        packages/plugins/slack/ → FAIL unexplained scope expansion",
		"    NOTE: Mechanical updates (package-lock.json, lockfile refreshes, lock bumps)",
		"    in isolation do not require PR description mention — use judgment",
		"    14e. If no scope claims are found in the PR description (no explicit scope language",
		"        from 14a, no Goals, no Tenets that imply a scope), do not FAIL Gate 8 for scope.",
		"        Instead note 'no scope claims found' and only FAIL if changes are clearly",
		"        unrelated to the PR intent. Scope ambiguity is informational, not a failure.",
		"",
		"OUTPUT FORMAT:",
		"",
		"Before the final VERDICT line, include exactly one machine-readable marker for each gate:",
		"<!-- skeptic-gate-1:PASS|FAIL -->  CI status",
		"<!-- skeptic-gate-2:PASS|FAIL -->  Mergeability",
		"<!-- skeptic-gate-3:PASS|FAIL -->  CodeRabbit",
		"<!-- skeptic-gate-4:PASS|FAIL -->  Bugbot/check-run plus unresolved blocking bugbot evidence",
		"<!-- skeptic-gate-5:PASS|FAIL -->  Review threads",
		"<!-- skeptic-gate-6:PASS|FAIL -->  Detailed evidence review; evidence exists is not enough",
		"<!-- skeptic-gate-7:PASS|FAIL -->  Independent technical skeptic review",
		"<!-- skeptic-gate-8:PASS|FAIL -->  PR description goals/tenets/scope vs code/evidence alignment",
		"<!-- skeptic-gate-8a:PASS|FAIL -->  Goals proof — behavioral goals have test validation",
		"<!-- skeptic-gate-8b:PASS|FAIL -->  Tenets adherence — all stated principles have implementing code",
		"<!-- skeptic-gate-8c:PASS|FAIL -->  Evidence provenance — evidence cited is tied to changed files",
		"<!-- skeptic-gate-8d:PASS|FAIL -->  Scope boundary — diff changes stay within stated scope",
		"A PASS verdict is invalid unless all eight primary markers (gates 1-8) are PASS.",
		"Gate 8 sub-markers (8a/8b/8c/8d) are informational — they do not independently gate the merge.",
		"Gate 8 is the merge gate; 8a/8b/8c/8d provide diagnostic detail and must be in PASS output.",
		"In PASS output, include 8a/8b/8c/8d (always emit these in a PASS verdict).",
		"In FAIL output, emit only the relevant sub-marker(s): 8a for Rule 12 gaps, 8b for Rule 13 gaps, 8c for evidence-provenance gaps, and 8d for Rule 14 gaps.",
		"",
		"// PASS — output brief confirmation only, no structured sections:",
		"<!-- skeptic-gate-1:PASS -->",
		"<!-- skeptic-gate-2:PASS -->",
		"<!-- skeptic-gate-3:PASS -->",
		"<!-- skeptic-gate-4:PASS -->",
		"<!-- skeptic-gate-5:PASS -->",
		"<!-- skeptic-gate-6:PASS -->",
		"<!-- skeptic-gate-7:PASS -->",
		"<!-- skeptic-gate-8:PASS -->",
		"<!-- skeptic-gate-8a:PASS -->",
		"<!-- skeptic-gate-8b:PASS -->",
		"<!-- skeptic-gate-8c:PASS -->",
		"<!-- skeptic-gate-8d:PASS -->",
		"VERDICT: PASS — [one sentence stating why the PR passes]",
		"--- // END PASS",
		"",
		"// FAIL — must include all four required sections in this exact order:",
		"## Background",
		"PR #[PR_NUMBER]: [title] — [what the PR claims to do]",
		"",
		"## Current Problem",
		"[Root cause — specific file, function, failure mode. Be concrete: 'function X in file Y uses stale cache' not 'cache issue']",
		"",
		"## Recommended Solution",
		"1. [Step 1 — specific and actionable]",
		"2. [Step 2]",
		"",
		"## Bot Consultation",
		"@coderabbitai — agree with this analysis?",
		"@cursor[bot] — does bugbot scan show the same?",
		"",
		"// Optional appendix sections — append after ## Bot Consultation if the corresponding rule has gaps:",
		"// ## Design Alignment     (include when Rule 11 gaps are found)",
		"// ## Goals Verification   (include when Rule 12 gaps are found — MUST quote specific goal and expected test evidence)",
		"// ## Tenets Adherence     (include when Rule 13 gaps are found — MUST quote specific tenet)",
		"// ## Scope Boundary       (include when Rule 14 gaps are found — MUST quote specific scope claim and diff evidence)",
		"// ## Gate 8 Sub-components (include when 8a/8b/8c/8d markers are present):",
		"//   <!-- skeptic-gate-8a:FAIL -->  Goals proof gap",
		"//   <!-- skeptic-gate-8b:FAIL -->  Tenets adherence gap",
		"//   <!-- skeptic-gate-8c:FAIL -->  Evidence provenance gap",
		"//   <!-- skeptic-gate-8d:FAIL -->  Scope boundary gap",
		"",
		"VERDICT: FAIL",
		"",
		"Be specific. 'The code looks fine' is NOT a valid PASS.",
		"Find at least one concrete gap before declaring FAIL.",
		"If every check genuinely passes, say so and explain why.",
		"--- PR CONTEXT ---",
		summary,
		prDescriptionSection,
		designDocSection,
		testFilesSection,
	}
	return strings.Join(body, "\n")
}

func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func verdictOrNotPosted(v string) string {
	if v == "" {
		return "not posted yet"
	}
	return v
}

func reviewSummaryLines(reviews []ReviewInfo, headSHA string) []string {
	shown := reviews
	if len(shown) > maxReviewsToShow {
		shown = shown[len(shown)-maxReviewsToShow:]
	}
	sorted := make([]ReviewInfo, len(shown))
	copy(sorted, shown)
	sort.SliceStable(sorted, func(i, j int) bool {
		return reviewTimeMillis(sorted[i].SubmittedAt) < reviewTimeMillis(sorted[j].SubmittedAt)
	})
	lines := make([]string, len(sorted))
	for i, r := range sorted {
		login := ""
		if r.Author != nil {
			login = r.Author.Login
		}
		anchor := ", unanchored"
		if r.CommitID != "" {
			if r.CommitID == headSHA {
				anchor = ", on-head"
			} else {
				anchor = ", stale:" + truncateRunes(r.CommitID, 7)
			}
		}
		body := r.Body
		if body == "" {
			body = "(no body)"
		}
		ts := r.SubmittedAt
		if len(ts) > 16 {
			ts = ts[:16]
		}
		lines[i] = fmt.Sprintf("[%s] %s (%s%s): %s", ts, login, r.State, anchor, truncateRunes(body, maxReviewBodyChars))
	}
	return lines
}

// TestFileContent is one test file's path and contents, kept as an ordered
// slice element (rather than a map) so BuildSkepticPrompt can preserve
// caller-supplied insertion order — mirrors TS's `Map<string, string>`,
// which iterates in insertion order.
type TestFileContent struct {
	Name    string
	Content string
}

func buildTestFilesSection(testFiles []TestFileContent) string {
	if len(testFiles) == 0 {
		return ""
	}
	lines := []string{"", "--- TEST FILE CONTENTS (for Rule 12 behavioral goal verification) ---"}
	remaining := maxTotalTestFileChars
	for _, tf := range testFiles {
		if remaining <= 0 {
			break
		}
		limit := maxTestFileChars
		if remaining < limit {
			limit = remaining
		}
		sliced := truncateRunes(tf.Content, limit)
		remaining -= len([]rune(sliced))
		lines = append(lines, fmt.Sprintf("--- %s ---", tf.Name), sliced)
	}
	return strings.Join(lines, "\n")
}
