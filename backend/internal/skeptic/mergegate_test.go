package skeptic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHasUnresolvedDismissedReview(t *testing.T) {
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "dismissed", CommitID: "sha1", SubmittedAt: "2026-01-02T00:00:00Z"},
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "changes_requested", CommitID: "sha1", SubmittedAt: "2026-01-01T00:00:00Z"},
	}
	if !hasUnresolvedDismissedReview(reviews, "sha1") {
		t.Fatal("expected true: newest review is dismissed with no subsequent approval")
	}
}

func TestHasUnresolvedDismissedReview_ApprovedAfterDismissedIsResolved(t *testing.T) {
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "approved", CommitID: "sha1", SubmittedAt: "2026-01-02T00:00:00Z"},
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "dismissed", CommitID: "sha1", SubmittedAt: "2026-01-01T00:00:00Z"},
	}
	if hasUnresolvedDismissedReview(reviews, "sha1") {
		t.Fatal("expected false: a newer approval resolves the earlier dismissal")
	}
}

func TestHasUnresolvedDismissedReview_IgnoresDifferentHeadSha(t *testing.T) {
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "dismissed", CommitID: "old-sha"},
	}
	if hasUnresolvedDismissedReview(reviews, "current-sha") {
		t.Fatal("expected false: dismissed review is on a different (stale) head SHA")
	}
}

func TestGetLatestDecisiveReview(t *testing.T) {
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "commented", CommitID: "sha1", SubmittedAt: "2026-01-03T00:00:00Z"},
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "changes_requested", CommitID: "sha1", SubmittedAt: "2026-01-01T00:00:00Z"},
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "approved", CommitID: "sha1", SubmittedAt: "2026-01-02T00:00:00Z"},
	}
	got := getLatestDecisiveReview(reviews, "sha1")
	if got == nil || got.State != "approved" {
		t.Fatalf("got %+v, want the newest decisive (approved/changes_requested) review, skipping 'commented'", got)
	}
}

func TestGetLatestDecisiveReview_NoneOnHead(t *testing.T) {
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "approved", CommitID: "stale-sha"},
	}
	if got := getLatestDecisiveReview(reviews, "current-sha"); got != nil {
		t.Fatalf("got %+v, want nil for a review on a different head SHA", got)
	}
}

func TestExtractSkepticRequestId(t *testing.T) {
	if got := extractSkepticRequestId("<!-- skeptic-request-id-abc123 -->"); got != "abc123" {
		t.Fatalf("got %q, want abc123", got)
	}
	if got := extractSkepticRequestId("no marker here"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestHasMatchingWorkflowTrigger(t *testing.T) {
	comments := []IssueComment{
		{
			Body: "SKEPTIC_GATE_TRIGGER\n<!-- skeptic-gate-trigger-abc123 -->\n<!-- skeptic-head-sha-abc123 -->\n<!-- skeptic-request-id-req1 -->",
			User: ReviewAuthor{Login: "github-actions[bot]"},
		},
	}
	verdictBody := "<!-- skeptic-gate-trigger-abc123 -->\nVERDICT: PASS"
	got := hasMatchingWorkflowTrigger(comments, verdictBody, "abc123", "req1")
	if got != "req1" {
		t.Fatalf("got %q, want req1", got)
	}
}

func TestHasMatchingWorkflowTrigger_NoMatchingComment(t *testing.T) {
	verdictBody := "<!-- skeptic-gate-trigger-abc123 -->\nVERDICT: PASS"
	got := hasMatchingWorkflowTrigger(nil, verdictBody, "abc123", "req1")
	if got != "" {
		t.Fatalf("got %q, want empty with no comments to match", got)
	}
}

func TestHasMatchingWorkflowTrigger_EmptyHeadShaOrRequestId(t *testing.T) {
	if got := hasMatchingWorkflowTrigger(nil, "body", "", "req1"); got != "" {
		t.Fatalf("got %q, want empty for empty headSHA", got)
	}
	if got := hasMatchingWorkflowTrigger(nil, "body", "sha", ""); got != "" {
		t.Fatalf("got %q, want empty for empty requestID", got)
	}
}

// mergeGateFakeGH dispatches ghRunner calls to different fixtures based on
// the endpoint/query shape, since FetchMergeGateState makes many different
// API calls in sequence.
type mergeGateFakeGH struct {
	prData           string // repos/o/r/pulls/N
	commitStatus     string // repos/o/r/commits/SHA/status
	checkRuns        string // repos/o/r/commits/SHA/check-runs (paginate)
	reviewsGraphQL   string // graphql query containing "reviews("
	threadsGraphQL   string // graphql query containing "reviewThreads("
	threadsErr       error
	commitDetail     string // repos/o/r/commits/SHA (no /status suffix)
	issueComments    string // repos/o/r/issues/N/comments (paginate)
	issueCommentsErr error
}

// findEndpoint returns the first arg that looks like a REST/GraphQL
// endpoint path (contains a "/") rather than a flag — robust to ghJSON's
// ["api", endpoint, ...] shape and ghJSONPaginate's
// ["api", "--paginate", "--slurp", endpoint, ...] shape, which put the
// endpoint at different indices. Matching purely on substrings of the full
// joined args string (as this dispatcher originally did for everything
// except the endpoint-shape checks) would over-match here since
// "/status"/"/comments" etc. need to test the ENDPOINT specifically, not
// any flag value — so route explicitly off the resolved endpoint string.
func findEndpoint(args []string) string {
	for _, a := range args {
		if a == "api" || a == "--paginate" || a == "--slurp" || a == "graphql" || a == "-f" || strings.HasPrefix(a, "query=") {
			continue
		}
		if strings.Contains(a, "/") {
			return a
		}
	}
	return ""
}

func (f *mergeGateFakeGH) run(ctx context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	endpoint := findEndpoint(args)
	switch {
	case strings.Contains(joined, "graphql") && strings.Contains(joined, "reviewThreads("):
		if f.threadsErr != nil {
			return nil, f.threadsErr
		}
		if f.threadsGraphQL == "" {
			return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`), nil
		}
		return []byte(f.threadsGraphQL), nil
	case strings.Contains(joined, "graphql") && strings.Contains(joined, "reviews(last:20)"):
		if f.reviewsGraphQL == "" {
			return []byte(`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`), nil
		}
		return []byte(f.reviewsGraphQL), nil
	case strings.Contains(endpoint, "/check-runs"):
		if f.checkRuns == "" {
			return []byte(`[]`), nil
		}
		return []byte(f.checkRuns), nil
	case strings.HasSuffix(endpoint, "/status"):
		return []byte(f.commitStatus), nil
	case strings.Contains(endpoint, "/issues/") && strings.Contains(endpoint, "/comments"):
		if f.issueCommentsErr != nil {
			return nil, f.issueCommentsErr
		}
		if f.issueComments == "" {
			return []byte(`[]`), nil
		}
		return []byte(f.issueComments), nil
	case strings.Count(endpoint, "/") == 4 && strings.Contains(endpoint, "/commits/"):
		// repos/o/r/commits/SHA (no further suffix)
		if f.commitDetail == "" {
			return []byte(`{}`), nil
		}
		return []byte(f.commitDetail), nil
	case strings.Contains(endpoint, "/pulls/"):
		return []byte(f.prData), nil
	}
	return []byte(`{}`), nil
}

func TestFetchMergeGateState_MissingHeadShaFailsClosed(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{prData: `{}`} // no head.sha
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	_, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err == nil {
		t.Fatal("expected an error when head SHA cannot be determined")
	}
}

func TestFetchMergeGateState_CIPassingFromCommitStatus(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{
		prData:       `{"head":{"sha":"abc123"},"mergeable":true,"merged":false,"user":{"login":"author1"}}`,
		commitStatus: `{"state":"success"}`,
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CIPassing || !got.NoConflicts {
		t.Fatalf("got %+v, want CIPassing=true NoConflicts=true", got)
	}
}

func TestFetchMergeGateState_ChecksAllCleanFallsBackToCIPassing(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{
		prData:       `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus: `{"state":"pending"}`, // raw status not success
		checkRuns:    `[{"check_runs":[{"name":"build","status":"completed","conclusion":"success"},{"name":"Skeptic Gate","status":"completed","conclusion":"failure"}]}]`,
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CIPassing {
		t.Fatalf("got %+v, want CIPassing=true (all real check runs clean, Skeptic Gate excluded)", got)
	}
	if len(got.CheckRuns) != 1 || got.CheckRuns[0].Name != "build" {
		t.Fatalf("got CheckRuns=%+v, want only 'build' (Skeptic Gate excluded)", got.CheckRuns)
	}
}

func TestFetchMergeGateState_FailedCheckRunKeepsCINotPassing(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{
		prData:       `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus: `{"state":"pending"}`,
		checkRuns:    `[{"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}]`,
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if got.CIPassing {
		t.Fatalf("got %+v, want CIPassing=false with a real failed check run", got)
	}
}

func TestFetchMergeGateState_CRApprovedFromReview(t *testing.T) {
	orig := ghRunner
	reviewsJSON, _ := json.Marshal(map[string]any{
		"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviews": map[string]any{"nodes": []map[string]any{
				{"author": map[string]any{"login": "coderabbitai"}, "state": "APPROVED", "body": "lgtm", "submittedAt": "2026-01-01T00:00:00Z", "commit": map[string]any{"oid": "abc123"}},
			}},
		}}},
	})
	f := &mergeGateFakeGH{
		prData:         `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:   `{"state":"success"}`,
		reviewsGraphQL: string(reviewsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CRApproved {
		t.Fatalf("got %+v, want CRApproved=true", got)
	}
}

func TestFetchMergeGateState_CRApprovedFromCommentFallback(t *testing.T) {
	orig := ghRunner
	commentsJSON, _ := json.Marshal([][]map[string]any{{
		{"id": 1, "body": "[approve]", "created_at": "2026-01-02T00:00:00Z", "user": map[string]any{"login": "coderabbitai"}},
	}})
	f := &mergeGateFakeGH{
		prData:        `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:  `{"state":"success"}`,
		commitDetail:  `{"commit":{"committer":{"date":"2026-01-01T00:00:00Z"}}}`,
		issueComments: string(commentsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CRApproved || got.CRState != "approved (comment)" {
		t.Fatalf("got %+v, want CRApproved=true via comment fallback", got)
	}
}

func TestFetchMergeGateState_ReviewThreadsRateLimitDegradesGracefully(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{
		prData:       `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus: `{"state":"success"}`,
		threadsErr:   errors.New("GraphQL: rate limit exceeded"),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatalf("expected graceful degradation, not an error: %v", err)
	}
	if got.UnresolvedBlockingComments != 0 || got.BugbotErrors != 0 {
		t.Fatalf("got %+v, want 0/0 on rate-limit degradation", got)
	}
}

func TestFetchMergeGateState_ReviewThreadsRealErrorFailsClosed(t *testing.T) {
	orig := ghRunner
	f := &mergeGateFakeGH{
		prData:       `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus: `{"state":"success"}`,
		threadsErr:   errors.New("connection reset"),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	_, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err == nil {
		t.Fatal("expected a real (non-rate-limit) review-threads error to fail closed, not silently degrade to 0")
	}
}

func TestFetchMergeGateState_NitCommentsExcludedFromUnresolvedCount(t *testing.T) {
	orig := ghRunner
	threadsJSON, _ := json.Marshal(map[string]any{
		"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes": []map[string]any{
					{"isResolved": false, "isOutdated": false, "comments": map[string]any{"nodes": []map[string]any{{"body": "nit: consider renaming", "author": map[string]any{"login": "reviewer"}}}}},
					{"isResolved": false, "isOutdated": false, "comments": map[string]any{"nodes": []map[string]any{{"body": "this is a real blocking issue", "author": map[string]any{"login": "reviewer"}}}}},
					{"isResolved": true, "isOutdated": false, "comments": map[string]any{"nodes": []map[string]any{{"body": "resolved thread, real issue but resolved", "author": map[string]any{"login": "reviewer"}}}}},
				},
			},
		}}},
	})
	f := &mergeGateFakeGH{
		prData:         `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:   `{"state":"success"}`,
		threadsGraphQL: string(threadsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if got.UnresolvedBlockingComments != 1 {
		t.Fatalf("got UnresolvedBlockingComments=%d, want 1 (nit excluded, resolved excluded)", got.UnresolvedBlockingComments)
	}
}

func TestFetchMergeGateState_ExistingVerdictFound(t *testing.T) {
	orig := ghRunner
	body := "<!-- skeptic-agent-verdict -->\nVERDICT: FAIL"
	commentsJSON, _ := json.Marshal([][]map[string]any{{
		{"id": 99, "body": body, "user": map[string]any{"login": "github-actions[bot]"}},
	}})
	f := &mergeGateFakeGH{
		prData:        `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:  `{"state":"success"}`,
		issueComments: string(commentsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if got.SkepticVerdict != "FAIL" || got.SkepticCommentID != 99 {
		t.Fatalf("got %+v, want SkepticVerdict=FAIL SkepticCommentID=99", got)
	}
}

func TestFetchMergeGateState_VerdictFromPRAuthorIsIgnored(t *testing.T) {
	orig := ghRunner
	body := "<!-- skeptic-agent-verdict -->\nVERDICT: FAIL"
	commentsJSON, _ := json.Marshal([][]map[string]any{{
		{"id": 99, "body": body, "user": map[string]any{"login": "author1"}}, // same as PR author
	}})
	f := &mergeGateFakeGH{
		prData:        `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:  `{"state":"success"}`,
		issueComments: string(commentsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "author1") // skepticBotAuthor == PR author login too
	if err != nil {
		t.Fatal(err)
	}
	if got.SkepticVerdict != "" {
		t.Fatalf("got SkepticVerdict=%q, want empty — a verdict authored by the PR's own author must be ignored", got.SkepticVerdict)
	}
}

// TestFetchMergeGateState_StalePassWithoutFreshContractIsIgnored is the
// security-relevant case: a PASS verdict comment missing its fresh-contract
// markers (request-id/head-sha/complete gate table) must not be trusted —
// only the LAST valid match in the loop matters, but a stale/incomplete
// PASS should never become that match at all.
func TestFetchMergeGateState_StalePassWithoutFreshContractIsIgnored(t *testing.T) {
	orig := ghRunner
	// PASS verdict with NO gate markers, no request-id, no head-sha marker.
	body := "<!-- skeptic-agent-verdict -->\nVERDICT: PASS"
	commentsJSON, _ := json.Marshal([][]map[string]any{{
		{"id": 99, "body": body, "user": map[string]any{"login": "github-actions[bot]"}},
	}})
	f := &mergeGateFakeGH{
		prData:        `{"head":{"sha":"abc123"},"mergeable":true,"user":{"login":"author1"}}`,
		commitStatus:  `{"state":"success"}`,
		issueComments: string(commentsJSON),
	}
	ghRunner = f.run
	t.Cleanup(func() { ghRunner = orig })

	got, err := FetchMergeGateState(ctx, "owner", "repo", 42, "github-actions[bot]")
	if err != nil {
		t.Fatal(err)
	}
	if got.SkepticVerdict != "" {
		t.Fatalf("got SkepticVerdict=%q, want empty — a stale/incomplete PASS must not be trusted", got.SkepticVerdict)
	}
}
