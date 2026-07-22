package skeptic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// nitPattern matches a leading "nit:" or "nitpick" prefix on a review
// comment — nit comments are excluded from unresolved-blocking-comment
// counts. Mirrors NIT_PATTERN in mergeGate.ts.
var nitPattern = regexp.MustCompile(`(?i)^(nit:|nitpick)`)

// evidenceBotLogin is the GitHub login of the evidence-review bot. Mirrors
// EVIDENCE_BOT in mergeGate.ts.
const evidenceBotLogin = "evidence-review-bot"

// hasUnresolvedDismissedReview reports whether the newest CodeRabbit review
// attached to headSHA is "dismissed" without a subsequent real "approved"
// review superseding it. Mirrors hasUnresolvedDismissedReview in
// mergeGate.ts.
func hasUnresolvedDismissedReview(reviews []ReviewInfo, headSHA string) bool {
	var crReviews []ReviewInfo
	for _, r := range reviews {
		if IsCodeRabbitReview(r) && r.CommitID == headSHA {
			crReviews = append(crReviews, r)
		}
	}
	if len(crReviews) == 0 {
		return false
	}
	sorted := sortReviewsNewestFirst(crReviews)
	for _, review := range sorted {
		state := strings.ToLower(review.State)
		if state == "dismissed" {
			return true
		}
		if state == "approved" {
			return false
		}
	}
	return false
}

// getLatestDecisiveReview returns the newest CodeRabbit review attached to
// headSHA whose state is "approved" or "changes_requested" (i.e. a
// decisive review, not a comment/pending/dismissed one), or nil if none.
// Mirrors getLatestDecisiveReview in mergeGate.ts — restricting to headSHA
// prevents a stale CHANGES_REQUESTED on a superseded head from causing a
// false-FAIL verdict, unlike GitHub's own UI-level reviewDecision (which
// reflects the worst state across ALL reviews, including stale ones).
func getLatestDecisiveReview(reviews []ReviewInfo, headSHA string) *ReviewInfo {
	var filtered []ReviewInfo
	for _, r := range reviews {
		state := strings.ToLower(r.State)
		if IsCodeRabbitReview(r) && (state == "approved" || state == "changes_requested") && r.CommitID == headSHA {
			filtered = append(filtered, r)
		}
	}
	sorted := sortReviewsNewestFirst(filtered)
	if len(sorted) == 0 {
		return nil
	}
	return &sorted[0]
}

var skepticRequestIDRe = regexp.MustCompile(`(?i)<!--\s*skeptic-request-id-([A-Za-z0-9_.:-]+)\s*-->`)

// extractSkepticRequestId extracts the skeptic-request-id marker value from
// body, or "" if none is present. Mirrors extractSkepticRequestId in
// mergeGate.ts.
func extractSkepticRequestId(body string) string {
	m := skepticRequestIDRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

// hasMatchingWorkflowTrigger reports whether comments contains a
// github-actions[bot] trigger comment matching verdictBody's head-sha and
// request-id AND the specific gate/cron trigger-type marker also present in
// verdictBody, returning the matched requestID (or "" if none matches).
// Mirrors hasMatchingWorkflowTrigger in mergeGate.ts. Both headSHA and
// requestID must be non-empty for any match to be attempted.
func hasMatchingWorkflowTrigger(comments []IssueComment, verdictBody, headSHA, requestID string) string {
	if headSHA == "" || requestID == "" {
		return ""
	}
	escapedSHA := EscapeRegexLiteral(headSHA)
	escapedRequestID := EscapeRegexLiteral(requestID)
	headShaRe := regexp.MustCompile(`(?i)<!--\s*skeptic-head-sha-` + escapedSHA + `\s*-->`)
	requestIDRe := regexp.MustCompile(`(?i)<!--\s*skeptic-request-id-` + escapedRequestID + `\s*-->`)

	var triggerTypes []string
	for _, triggerType := range []string{"gate", "cron"} {
		re := regexp.MustCompile(`(?i)<!--\s*skeptic-` + triggerType + `-trigger-` + escapedSHA + `\s*-->`)
		if re.MatchString(verdictBody) {
			triggerTypes = append(triggerTypes, triggerType)
		}
	}
	if len(triggerTypes) == 0 {
		return ""
	}

	for _, triggerType := range triggerTypes {
		triggerLabelRe := regexp.MustCompile(`(?i)SKEPTIC_` + strings.ToUpper(triggerType) + `_TRIGGER`)
		triggerMarkerRe := regexp.MustCompile(`(?i)<!--\s*skeptic-` + triggerType + `-trigger-` + escapedSHA + `\s*-->`)
		for _, c := range comments {
			if !strings.EqualFold(c.User.Login, "github-actions[bot]") {
				continue
			}
			if triggerLabelRe.MatchString(c.Body) &&
				triggerMarkerRe.MatchString(c.Body) &&
				headShaRe.MatchString(c.Body) &&
				requestIDRe.MatchString(c.Body) {
				return requestID
			}
		}
	}
	return ""
}

// ghAPIGet is a small helper: fetch endpoint via ghJSON and unmarshal into
// dest.
func ghAPIGet(ctx context.Context, endpoint string, dest any) error {
	raw, err := ghJSON(ctx, endpoint)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// FetchMergeGateState fetches the aggregated PR readiness state fed into the
// skeptic evaluation prompt: CI status, mergeability, CodeRabbit review
// state, unresolved/bugbot review-thread counts, evidence-review approval,
// and any prior skeptic verdict already posted. Mirrors fetchMergeGateState
// in mergeGate.ts section-by-section (see inline comments below for the
// section boundaries, matching the TS source's own numbered comments).
//
// This is read-only PR/CI state fetching — distinct from, and never calling,
// packages/core/src/merge-gate.ts's checkMergeGate (the AO daemon's own
// internal lifecycle-decision engine used by
// lifecycle-manager.ts/terminal-guard.ts/reaction-context.ts/action-plan.ts),
// which stays out of scope for this port per team-lead's original
// architecture decision.
func FetchMergeGateState(ctx context.Context, owner, repo string, prNumber int, skepticBotAuthor string) (MergeGateState, error) {
	// 1. CI status + mergeability — single call to /pulls/{prNumber}, extract
	// both. Mirrors the TS source's single try/catch spanning both the PR
	// fetch and the commit-status fetch: if EITHER fails, ciPassing/
	// noConflicts/checkRuns all stay at their zero values and headSha stays
	// unset, which triggers the fail-closed error below.
	var (
		ciPassing    bool
		ciRawState   = "unknown"
		noConflicts  bool
		mergeableRaw *bool
		checkRuns    []CheckRunSummary
		headSHA      string
		prAuthor     string
	)
	func() {
		var prData struct {
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Mergeable *bool `json:"mergeable"`
			Merged    bool  `json:"merged"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := ghAPIGet(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, prNumber), &prData); err != nil {
			return
		}
		mergeableRaw = prData.Mergeable
		noConflicts = (prData.Mergeable != nil && *prData.Mergeable) || prData.Merged
		headSHA = prData.Head.SHA
		prAuthor = prData.User.Login

		if headSHA == "" {
			return
		}
		var commitStatus struct {
			State string `json:"state"`
		}
		if err := ghAPIGet(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/status", owner, repo, headSHA), &commitStatus); err != nil {
			// TS: a failure here is inside the SAME outer try as the prData
			// fetch, so headSha effectively becomes moot (the whole section
			// is treated as failed) — but headSha was already assigned
			// above in TS too (`let headSha` is captured by reference in the
			// outer scope even if this inner call throws), so the
			// fail-closed headSha check below still sees a real value and
			// does NOT trigger. Only ciPassing/ciRawState/checkRuns are
			// affected. Mirrored by simply not touching those on error here
			// too and returning early — checkRuns fetch below is skipped by
			// virtue of not being reached, matching the TS control flow of
			// throwing out of the whole outer try block.
			return
		}
		ciRawState = commitStatus.State
		if ciRawState == "" {
			ciRawState = "unknown"
		}
		ciPassing = commitStatus.State == "success"

		// Fetch individual check runs for independent verification
		// (paginated to capture all pages). Non-fatal on failure.
		func() {
			raw, err := ghJSONPaginate(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?per_page=100", owner, repo, headSHA))
			if err != nil {
				return
			}
			var pages []struct {
				CheckRuns []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"check_runs"`
			}
			if jsonErr := json.Unmarshal(raw, &pages); jsonErr != nil {
				return
			}
			skepticGateCheckRe := regexp.MustCompile(`(?i)^Skeptic\s+Gate$`)
			// Exclude the self-referential "Skeptic Gate" check — its
			// failure is the mechanism that polls for THIS verdict,
			// creating a circular dependency.
			type seenEntry struct {
				summary CheckRunSummary
			}
			seen := map[string]seenEntry{}
			var order []string
			for _, page := range pages {
				for _, run := range page.CheckRuns {
					if skepticGateCheckRe.MatchString(run.Name) {
						continue
					}
					existing, ok := seen[run.Name]
					// Prefer completed runs over in-progress.
					if !ok || (run.Status == "completed" && existing.summary.Status != "completed") {
						seen[run.Name] = seenEntry{summary: CheckRunSummary{Name: run.Name, Status: run.Status, Conclusion: run.Conclusion}}
						if !ok {
							order = append(order, run.Name)
						}
					}
				}
			}
			for _, name := range order {
				checkRuns = append(checkRuns, seen[name].summary)
			}
			if !ciPassing && len(checkRuns) > 0 {
				hasFailedChecks := false
				hasPendingChecks := false
				for _, run := range checkRuns {
					if run.Status == "completed" &&
						run.Conclusion != "success" &&
						run.Conclusion != "skipped" &&
						run.Conclusion != "neutral" &&
						run.Conclusion != "cancelled" {
						hasFailedChecks = true
					}
					if run.Status != "completed" {
						hasPendingChecks = true
					}
				}
				if !hasFailedChecks && !hasPendingChecks {
					ciPassing = true
				}
			}
		}()
	}()

	// Fail-closed: headSha is mandatory for checking reviews and CI.
	if headSHA == "" {
		return MergeGateState{}, fmt.Errorf("could not determine head SHA for PR #%d", prNumber)
	}

	// 2. CR review state — mirrors checkMergeGate + merge-gate-coderabbit.ts.
	reviews, err := FetchReviews(ctx, owner, repo, prNumber)
	if err != nil {
		return MergeGateState{}, err
	}
	// CRITICAL: filter by head SHA — GitHub's UI-level reviewDecision
	// returns the worst state across ALL reviews, including stale
	// CHANGES_REQUESTED reviews on superseded head SHAs.
	latestCR := getLatestDecisiveReview(reviews, headSHA)
	crDismissedWithoutApproval := hasUnresolvedDismissedReview(reviews, headSHA)

	var crApproved bool
	var crState string
	if latestCR != nil {
		crState = latestCR.State
		crApproved = strings.EqualFold(latestCR.State, "approved") && !crDismissedWithoutApproval
	} else {
		// No on-head decisive review. Surface this explicitly so the LLM
		// doesn't fall back to GitHub's UI-level reviewDecision (which
		// reflects stale reviews on old SHAs).
		crState = "none-on-head"
		crApproved = false
	}

	// Fallback: check comments for CodeRabbit's [approve] comment if review
	// state is not approved.
	if !crApproved {
		func() {
			var commitData struct {
				Commit struct {
					Committer struct {
						Date string `json:"date"`
					} `json:"committer"`
				} `json:"commit"`
			}
			if err := ghAPIGet(ctx, fmt.Sprintf("repos/%s/%s/commits/%s", owner, repo, headSHA), &commitData); err != nil {
				return
			}
			headCommittedAt := commitData.Commit.Committer.Date
			if headCommittedAt == "" {
				return
			}
			headTimeMillis, timeErr := parseRFC3339Lenient(headCommittedAt)
			if timeErr != nil {
				return
			}
			raw, pageErr := ghJSONPaginate(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, prNumber))
			if pageErr != nil {
				return
			}
			var pages [][]struct {
				ID        int    `json:"id"`
				Body      string `json:"body"`
				CreatedAt string `json:"created_at"`
				User      struct {
					Login string `json:"login"`
				} `json:"user"`
			}
			if jsonErr := json.Unmarshal(raw, &pages); jsonErr != nil {
				return
			}
			hasApproveComment := false
			for _, page := range pages {
				for _, c := range page {
					author := strings.ToLower(c.User.Login)
					isCR := author == "coderabbitai" || author == "coderabbitai[bot]"
					if !isCR || c.Body == "" {
						continue
					}
					commentTimeMillis, ctErr := parseRFC3339Lenient(c.CreatedAt)
					if ctErr != nil || commentTimeMillis < headTimeMillis {
						continue
					}
					if approveCommentRe.MatchString(c.Body) {
						hasApproveComment = true
						break
					}
				}
				if hasApproveComment {
					break
				}
			}
			if hasApproveComment {
				crApproved = true
				crState = "approved (comment)"
			}
		}()
	}

	// 3. Review threads — nit-filtered unresolved counts (matches
	// checkMergeGate). Uses GraphQL reviewThreads.isResolved (REST
	// /pulls/{n}/comments has no state field). Errors are NOT swallowed
	// here except for the specific rate-limit/GraphQL-error case (fail
	// closed: if we cannot determine comment state at all, we must not
	// silently report 0 unresolved comments, which would bypass Gate 5) —
	// UNLESS the failure looks like a rate limit or GraphQL error, in which
	// case TS explicitly degrades to 0/0 rather than aborting the whole
	// function.
	bugbotErrors, unresolvedBlockingComments, threadErr := fetchReviewThreadCounts(ctx, owner, repo, prNumber)
	if threadErr != nil {
		if isRateLimitOrGraphQLError(threadErr.Error()) {
			bugbotErrors = 0
			unresolvedBlockingComments = 0
		} else {
			return MergeGateState{}, threadErr
		}
	}

	// 4. Evidence review.
	var evidenceReviews []ReviewInfo
	for _, r := range reviews {
		if r.Author != nil && r.Author.Login == evidenceBotLogin {
			evidenceReviews = append(evidenceReviews, r)
		}
	}
	sortedEvidence := sortReviewsNewestFirst(evidenceReviews)
	evidenceApproved := len(sortedEvidence) > 0 && strings.EqualFold(sortedEvidence[0].State, "approved")
	evidenceRequired := false // controlled via config; default false for skeptic CLI

	// 5. Existing skeptic verdict — use paginated fetch to capture all pages
	// of comments.
	skepticVerdict, skepticCommentID := findExistingSkepticVerdict(ctx, owner, repo, prNumber, skepticBotAuthor, headSHA, prAuthor)

	return MergeGateState{
		CIPassing:                  ciPassing,
		CIRawState:                 ciRawState,
		CheckRuns:                  checkRuns,
		NoConflicts:                noConflicts,
		MergeableRaw:               mergeableRaw,
		CRApproved:                 crApproved,
		CRState:                    crState,
		CRDismissedWithoutApproval: crDismissedWithoutApproval,
		BugbotErrors:               bugbotErrors,
		UnresolvedBlockingComments: unresolvedBlockingComments,
		EvidenceApproved:           evidenceApproved,
		EvidenceRequired:           evidenceRequired,
		SkepticVerdict:             skepticVerdict,
		SkepticCommentID:           skepticCommentID,
	}, nil
}

var approveCommentRe = regexp.MustCompile(`(?im)^\s*\[approve\]\s*$|\bchanges approved\.`)

// fetchReviewThreadCounts paginates through all PR review threads via
// GraphQL and counts bugbot errors + unresolved blocking (non-nit) comments
// among non-resolved, non-outdated threads.
func fetchReviewThreadCounts(ctx context.Context, owner, repo string, prNumber int) (bugbotErrors, unresolvedBlockingComments int, err error) {
	type threadNode struct {
		IsResolved bool `json:"isResolved"`
		IsOutdated bool `json:"isOutdated"`
		Comments   struct {
			Nodes []struct {
				Body   string `json:"body"`
				Author *struct {
					Login string `json:"login"`
				} `json:"author"`
			} `json:"nodes"`
		} `json:"comments"`
	}

	var allNodes []threadNode
	cursor := ""
	hasNextPage := true
	for hasNextPage {
		afterArg := ""
		if cursor != "" {
			afterArg = fmt.Sprintf(`, after:%q`, cursor)
		}
		query := fmt.Sprintf(
			"{\n  repository(owner:%q, name:%q) {\n    pullRequest(number:%d) {\n      reviewThreads(first:100%s) {\n        pageInfo { hasNextPage endCursor }\n        nodes {\n          isResolved\n          isOutdated\n          comments(first:1) {\n            nodes { body author { login } }\n          }\n        }\n      }\n    }\n  }\n}",
			owner, repo, prNumber, afterArg,
		)
		raw, ghErr := ghJSON(ctx, "graphql", "-f", "query="+query)
		if ghErr != nil {
			return 0, 0, ghErr
		}
		var resp struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []threadNode `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if jsonErr := json.Unmarshal(raw, &resp); jsonErr != nil {
			return 0, 0, fmt.Errorf("decode review threads response: %w", jsonErr)
		}
		page := resp.Data.Repository.PullRequest.ReviewThreads
		allNodes = append(allNodes, page.Nodes...)
		hasNextPage = page.PageInfo.HasNextPage
		cursor = page.PageInfo.EndCursor
	}

	for _, t := range allNodes {
		if t.IsResolved || t.IsOutdated {
			continue
		}
		var body, author string
		if len(t.Comments.Nodes) > 0 {
			body = t.Comments.Nodes[0].Body
			if t.Comments.Nodes[0].Author != nil {
				author = t.Comments.Nodes[0].Author.Login
			}
		}
		isNit := nitPattern.MatchString(strings.TrimLeft(body, " \t\n\r"))
		isBugbot := strings.Contains(strings.ToLower(author), "cursor[bot]") && regexp.MustCompile(`(?i)error`).MatchString(body)

		if isBugbot {
			bugbotErrors++
		}
		if !isNit {
			unresolvedBlockingComments++
		}
	}
	return bugbotErrors, unresolvedBlockingComments, nil
}

// findExistingSkepticVerdict scans all PR comments for the newest valid
// skeptic verdict comment: authored by skepticBotAuthor or
// github-actions[bot] (the GHA runner posts SKIPPED fallback verdicts as
// github-actions[bot] when ao skeptic verify has no API keys), carrying the
// skeptic-agent-verdict marker (prevents a spoofed verdict from a malicious
// actor with write access to the bot account), never authored by the PR's
// own author, and — for a claimed PASS specifically — satisfying the fresh-
// verdict contract (matching request-id/head-sha markers plus a complete
// gate table) and a matching workflow trigger comment. Returns ("", 0) if
// none found. Mirrors the "5. Existing skeptic verdict" section of
// fetchMergeGateState in mergeGate.ts.
func findExistingSkepticVerdict(ctx context.Context, owner, repo string, prNumber int, skepticBotAuthor, headSHA, prAuthor string) (verdict string, commentID int) {
	raw, err := ghJSONPaginate(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, prNumber))
	if err != nil {
		return "", 0
	}
	var pages [][]struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if jsonErr := json.Unmarshal(raw, &pages); jsonErr != nil {
		return "", 0
	}
	var comments []IssueComment
	for _, page := range pages {
		for _, c := range page {
			comments = append(comments, IssueComment{ID: c.ID, Body: c.Body, User: ReviewAuthor{Login: c.User.Login}})
		}
	}

	acceptedAuthors := map[string]bool{
		strings.ToLower(skepticBotAuthor): true,
		"github-actions[bot]":             true,
	}
	agentVerdictMarkerRe := regexp.MustCompile(`(?i)<!--\s*skeptic-agent-verdict\s*-->`)

	for _, c := range comments {
		authorLogin := strings.ToLower(c.User.Login)
		if authorLogin == "" || !acceptedAuthors[authorLogin] {
			continue
		}
		if !agentVerdictMarkerRe.MatchString(c.Body) {
			continue
		}
		m := VerdictLineRegex.FindStringSubmatch(c.Body)
		if m == nil {
			continue
		}
		parsedVerdict := strings.ToUpper(m[1])
		verdictRequestID := extractSkepticRequestId(c.Body)
		authorIsPRAuthor := prAuthor != "" && strings.EqualFold(authorLogin, prAuthor)
		if authorIsPRAuthor {
			continue
		}
		if parsedVerdict == "PASS" &&
			(!IsFreshPassVerdictContractSatisfied(c.Body, headSHA, verdictRequestID) ||
				hasMatchingWorkflowTrigger(comments, c.Body, headSHA, verdictRequestID) == "") {
			continue
		}
		verdict = parsedVerdict
		commentID = c.ID
		// Do NOT break — keep iterating so the last match reflects the
		// newest verdict.
	}
	return verdict, commentID
}
