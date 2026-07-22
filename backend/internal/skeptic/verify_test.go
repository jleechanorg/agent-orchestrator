package skeptic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/skeptic/llmeval"
)

func TestNormalizeTriggerSHA(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"abc123f", "abc123f"},
		{"  abc123f  ", "abc123f"},
		{"ABC123F", "ABC123F"},
		{"not-a-sha", ""},
		{"abc12", ""}, // too short (< 7 chars)
	}
	for _, c := range cases {
		if got := NormalizeTriggerSHA(c.in); got != c.want {
			t.Errorf("NormalizeTriggerSHA(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveRepo_ExplicitFlag(t *testing.T) {
	owner, repo, err := ResolveRepo(ctx, "jleechanorg/agent-orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "jleechanorg" || repo != "agent-orchestrator" {
		t.Fatalf("got owner=%q repo=%q", owner, repo)
	}
}

func TestResolveRepo_InvalidFlag(t *testing.T) {
	if _, _, err := ResolveRepo(ctx, "not-owner-slash-repo"); err == nil {
		t.Fatal("expected error for malformed --repo flag")
	}
}

func TestResolveRepo_FallsBackToGhRepoView(t *testing.T) {
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "repo" && args[1] == "view" {
			return []byte(`{"owner":{"login":"someowner"},"name":"somerepo"}`), nil
		}
		return nil, errors.New("unexpected call: " + args[0])
	})
	owner, repo, err := ResolveRepo(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "someowner" || repo != "somerepo" {
		t.Fatalf("got owner=%q repo=%q", owner, repo)
	}
}

func TestResolveRepo_GhFailurePropagates(t *testing.T) {
	withFakeGH(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh: not a git repository")
	})
	if _, _, err := ResolveRepo(ctx, ""); err == nil {
		t.Fatal("expected error when gh repo view fails")
	}
}

// fakeIssueCommentsJSON builds REST-shaped JSON matching FetchIssueComments'
// decode target (snake_case created_at, nested user.login).
func fakeIssueCommentsJSON(t *testing.T, comments []IssueComment) []byte {
	t.Helper()
	type restUser struct {
		Login string `json:"login"`
	}
	type restComment struct {
		ID        int      `json:"id"`
		Body      string   `json:"body"`
		User      restUser `json:"user"`
		CreatedAt string   `json:"created_at"`
	}
	page := make([]restComment, len(comments))
	for i, c := range comments {
		page[i] = restComment{ID: c.ID, Body: c.Body, User: restUser{Login: c.User.Login}, CreatedAt: "2026-01-01T00:00:00Z"}
	}
	// FetchIssueComments decodes ghJSONPaginate's --slurp output, which
	// wraps each page's array as one element of an outer array.
	raw, err := json.Marshal([][]restComment{page})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFindExistingVerdict_MatchesBySHAAndReturnsCommentID(t *testing.T) {
	comments := []IssueComment{
		{ID: 1, Body: "unrelated comment"},
		{ID: 2, Body: "<!-- skeptic-agent-verdict -->\n<!-- skeptic-gate-trigger-abcdef1 -->\nVERDICT: PASS"},
	}
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return fakeIssueCommentsJSON(t, comments), nil
	})
	got, err := FindExistingVerdict(ctx, "o", "r", 1, "abcdef1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected an existing verdict, got nil")
	}
	if got.CommentID != 2 || got.Verdict != "PASS" {
		t.Fatalf("got %+v", got)
	}
}

func TestFindExistingVerdict_SHAMismatchSkipped(t *testing.T) {
	comments := []IssueComment{
		{ID: 2, Body: "<!-- skeptic-agent-verdict -->\n<!-- skeptic-gate-trigger-deadbeef -->\nVERDICT: PASS"},
	}
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return fakeIssueCommentsJSON(t, comments), nil
	})
	got, err := FindExistingVerdict(ctx, "o", "r", 1, "abcdef1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no match on SHA mismatch, got %+v", got)
	}
}

func TestFindExistingVerdict_NoTriggerSHAMatchesAny(t *testing.T) {
	comments := []IssueComment{
		{ID: 5, Body: "<!-- skeptic-agent-verdict -->\nVERDICT: FAIL — x"},
	}
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return fakeIssueCommentsJSON(t, comments), nil
	})
	got, err := FindExistingVerdict(ctx, "o", "r", 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.CommentID != 5 || got.Verdict != "FAIL" {
		t.Fatalf("got %+v", got)
	}
}

func TestFindExistingVerdict_RequestIDMismatchSkipped(t *testing.T) {
	comments := []IssueComment{
		{ID: 5, Body: "<!-- skeptic-agent-verdict -->\n<!-- skeptic-request-id-req-999 -->\nVERDICT: PASS"},
	}
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return fakeIssueCommentsJSON(t, comments), nil
	})
	got, err := FindExistingVerdict(ctx, "o", "r", 1, "", "req-111")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no match on request-id mismatch, got %+v", got)
	}
}

func TestAllFilesExcluded(t *testing.T) {
	diff := "diff --git a/docs/x.md b/docs/x.md\n--- a/docs/x.md\n+++ b/docs/x.md\n"
	if !AllFilesExcluded(diff, []string{"**/*.md"}) {
		t.Fatal("expected all-excluded for a docs-only diff against **/*.md")
	}
	mixedDiff := diff + "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n"
	if AllFilesExcluded(mixedDiff, []string{"**/*.md"}) {
		t.Fatal("expected NOT all-excluded once a non-matching file is present")
	}
	if AllFilesExcluded(diff, nil) {
		t.Fatal("empty pattern list must never trigger a skip")
	}
	if AllFilesExcluded("", []string{"**/*.md"}) {
		t.Fatal("empty diff must never trigger a skip")
	}
}

func TestApplyEvidenceOverride_ForcesFailOnInauthenticEvidencePass(t *testing.T) {
	verdict := "some reasoning\nVERDICT: PASS\n<!-- skeptic-gate-1:PASS -->"
	// No "## Evidence" heading at all -> inauthentic.
	got := ApplyEvidenceOverride(verdict, "PR body with no evidence section")
	if got == verdict {
		t.Fatal("expected verdict to be overridden")
	}
	m := VerdictLineRegex.FindStringSubmatch(got)
	if m == nil || m[1] != "FAIL" {
		t.Fatalf("expected overridden verdict line to be FAIL, got: %s", got)
	}
	if !containsAll(got, "<!-- skeptic-gate-1:FAIL -->") {
		t.Fatalf("expected PASS gate marker rewritten to FAIL, got: %s", got)
	}
}

func TestApplyEvidenceOverride_LeavesAuthenticPassAlone(t *testing.T) {
	prBody := "## Evidence\nReal proof: ran the test suite, output attached."
	verdict := "VERDICT: PASS"
	got := ApplyEvidenceOverride(verdict, prBody)
	if got != verdict {
		t.Fatalf("expected verdict unchanged for authentic evidence, got: %s", got)
	}
}

func TestApplyEvidenceOverride_LeavesFailAlone(t *testing.T) {
	// Inauthentic evidence + a FAIL verdict: nothing to override (already FAIL).
	verdict := "VERDICT: FAIL — something else went wrong"
	got := ApplyEvidenceOverride(verdict, "no evidence section")
	if got != verdict {
		t.Fatalf("expected FAIL verdict unchanged, got: %s", got)
	}
}

// Mutation check: ReplaceAllString (global) instead of replaceFirstMatch
// would silently do the same thing here since there's only one VERDICT
// line, but this test locks the single-VERDICT-line assumption in and
// documents *why* replaceFirstMatch exists instead of the simpler global
// replace the rest of this codebase uses everywhere else.
func TestApplyEvidenceOverride_OnlyRewritesFirstVerdictLine(t *testing.T) {
	// Two lines both match the (line-anchored) VerdictLineRegex — trailing
	// text after PASS must follow a "-"/"—"/":" separator to match at all
	// (see llmeval.LineRegex's `(?:[-—:].*)?$` tail), so both lines here
	// use that separator to genuinely match. A global replace would rewrite
	// both; TS's non-global .replace (and this port's replaceFirstMatch)
	// rewrites only the first.
	verdict := "VERDICT: PASS\nVERDICT: PASS — duplicate line from a confused LLM"
	got := ApplyEvidenceOverride(verdict, "no evidence section")
	if !containsAll(got, "VERDICT: FAIL — evidence authenticity check failed") {
		t.Fatalf("expected first VERDICT line replaced, got: %s", got)
	}
	// The second "VERDICT: PASS" line must survive untouched.
	if !containsAll(got, "VERDICT: PASS — duplicate line from a confused LLM") {
		t.Fatalf("expected second VERDICT line preserved untouched, got: %s", got)
	}
}

func TestRunEvaluation_DefaultChainOnEmptyModel(t *testing.T) {
	var called []llmeval.Model
	runners := llmeval.Runners{
		llmeval.ModelCodex: func(_ context.Context, _ string) llmeval.Result {
			called = append(called, llmeval.ModelCodex)
			return llmeval.Result{ValidVerdict: true, Output: "VERDICT: PASS"}
		},
	}
	got, err := RunEvaluation(ctx, runners, "prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "VERDICT: PASS" {
		t.Fatalf("got %q", got)
	}
	if len(called) != 1 || called[0] != llmeval.ModelCodex {
		t.Fatalf("expected codex to run first on default chain, got %v", called)
	}
}

func TestRunEvaluation_RotatesToPreferredModel(t *testing.T) {
	var called []llmeval.Model
	mk := func(m llmeval.Model) llmeval.Runner {
		return func(_ context.Context, _ string) llmeval.Result {
			called = append(called, m)
			return llmeval.Result{ValidVerdict: true, Output: "VERDICT: PASS from " + string(m)}
		}
	}
	runners := llmeval.Runners{
		llmeval.ModelCodex:  mk(llmeval.ModelCodex),
		llmeval.ModelGemini: mk(llmeval.ModelGemini),
	}
	got, err := RunEvaluation(ctx, runners, "prompt", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if got != "VERDICT: PASS from gemini" {
		t.Fatalf("got %q", got)
	}
	if len(called) != 1 || called[0] != llmeval.ModelGemini {
		t.Fatalf("expected gemini to run first when --model gemini given, got %v", called)
	}
}

func TestRunEvaluation_RejectsUnsupportedModel(t *testing.T) {
	if _, err := RunEvaluation(ctx, llmeval.DefaultRunners(), "prompt", "not-a-real-model"); err == nil {
		t.Fatal("expected error for unsupported --model value")
	}
}
