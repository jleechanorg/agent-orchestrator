package skeptic

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestExtractSkepticGateMarkers(t *testing.T) {
	body := "some text\n<!-- skeptic-gate-1:PASS -->\nmore\n<!-- skeptic-gate-8a:FAIL -->\ndone"
	got := ExtractSkepticGateMarkers(body)
	want := []string{"<!-- skeptic-gate-1:PASS -->", "<!-- skeptic-gate-8a:FAIL -->"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExtractSkepticGateMarkers_NoneFound(t *testing.T) {
	if got := ExtractSkepticGateMarkers("no markers here"); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func withFakePosting(t *testing.T, patch func(ctx context.Context, owner, repo string, commentID int, body string) error, create func(ctx context.Context, owner, repo string, prNumber int, body string) (string, error)) {
	t.Helper()
	origRunner := ghRunner
	// PatchComment/CreateComment shell out via ghRunner directly; intercept
	// at that layer and dispatch based on the argv shape (--method PATCH vs
	// a plain POST-style call), rather than needing separate injection
	// points for each — mirrors how the real functions are implemented.
	ghRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		isPatch := len(args) > 1 && args[1] == "--method"
		if isPatch {
			// args: ["api", "--method", "PATCH", "repos/o/r/issues/comments/ID", "--field", "body=..."]
			endpoint := args[3]
			idStr := endpoint[strings.LastIndex(endpoint, "/")+1:]
			id, _ := strconv.Atoi(idStr)
			body := strings.TrimPrefix(args[5], "body=")
			return nil, patch(ctx, "owner", "repo", id, body)
		}
		// args: ["api", "repos/o/r/issues/N/comments", "--field", "body=..."]
		body := strings.TrimPrefix(args[3], "body=")
		_, err := create(ctx, "owner", "repo", 0, body)
		return nil, err
	}
	t.Cleanup(func() { ghRunner = origRunner })
}

func TestPostVerdict_CreatesWhenNoExistingComment(t *testing.T) {
	var createdBody string
	withFakePosting(t,
		func(context.Context, string, string, int, string) error {
			t.Fatal("patch should not be called")
			return nil
		},
		func(_ context.Context, _, _ string, _ int, body string) (string, error) {
			createdBody = body
			return body, nil
		},
	)
	got, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 0, "github-actions[bot]", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != createdBody {
		t.Fatalf("returned body should be the exact posted body")
	}
	if !strings.Contains(got, "<!-- skeptic-agent-verdict -->") {
		t.Fatalf("missing verdict marker in body: %s", got)
	}
	if !strings.Contains(got, "VERDICT: PASS") {
		t.Fatalf("missing verdict text in body: %s", got)
	}
}

func TestPostVerdict_UpdatesExistingComment(t *testing.T) {
	patched := false
	withFakePosting(t,
		func(context.Context, string, string, int, string) error { patched = true; return nil },
		func(context.Context, string, string, int, string) (string, error) {
			t.Fatal("create should not be called")
			return "", nil
		},
	)
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 999, "github-actions[bot]", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !patched {
		t.Fatal("expected patch to be called for an existing comment")
	}
}

func TestPostVerdict_FallsBackToCreateOn404(t *testing.T) {
	created := false
	withFakePosting(t,
		func(context.Context, string, string, int, string) error { return errors.New("HTTP 404: Not Found") },
		func(context.Context, string, string, int, string) (string, error) { created = true; return "", nil },
	)
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 999, "github-actions[bot]", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a CREATE fallback on 404")
	}
}

func TestPostVerdict_FallsBackToCreateOnCrossUserEditConflict(t *testing.T) {
	created := false
	withFakePosting(t,
		func(context.Context, string, string, int, string) error {
			return errors.New("HTTP 403: Forbidden — you must be the author to edit this comment")
		},
		func(context.Context, string, string, int, string) (string, error) { created = true; return "", nil },
	)
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 999, "github-actions[bot]", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a CREATE fallback on a 403 edit conflict")
	}
}

func TestPostVerdict_DoesNotFallBackOnAuthOr403RateLimit(t *testing.T) {
	withFakePosting(t,
		func(context.Context, string, string, int, string) error {
			return errors.New("403 Forbidden: rate limit exceeded")
		},
		func(context.Context, string, string, int, string) (string, error) {
			t.Fatal("create should not be called")
			return "", nil
		},
	)
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 999, "github-actions[bot]", "", "", nil)
	if err == nil {
		t.Fatal("expected the rate-limited 403 to propagate, not silently fall back to CREATE")
	}
}

func TestPostVerdict_RethrowsOtherPatchErrors(t *testing.T) {
	withFakePosting(t,
		func(context.Context, string, string, int, string) error { return errors.New("network timeout") },
		func(context.Context, string, string, int, string) (string, error) {
			t.Fatal("create should not be called")
			return "", nil
		},
	)
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 999, "github-actions[bot]", "", "", nil)
	if err == nil {
		t.Fatal("expected the network error to propagate")
	}
}

func TestPostVerdict_IncludesBindingMarkers(t *testing.T) {
	var body string
	withFakePosting(t,
		nil,
		func(_ context.Context, _, _ string, _ int, b string) (string, error) { body = b; return b, nil },
	)
	binding := &SkepticVerdictBinding{RequestID: "req-123", HeadSHA: "abc1234"}
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 0, "github-actions[bot]", "abc1234", "", binding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "<!-- skeptic-request-id-req-123 -->") {
		t.Fatalf("missing request-id marker: %s", body)
	}
	if !strings.Contains(body, "<!-- skeptic-head-sha-abc1234 -->") {
		t.Fatalf("missing head-sha marker: %s", body)
	}
	if !strings.Contains(body, "<!-- skeptic-gate-trigger-abc1234 -->") {
		t.Fatalf("missing gate-trigger marker: %s", body)
	}
	if !strings.Contains(body, "<!-- skeptic-cron-trigger-abc1234 -->") {
		t.Fatalf("missing cron-trigger marker: %s", body)
	}
}

func TestPostVerdict_IncludesFullLLMOutputWhenDifferentFromVerdict(t *testing.T) {
	var body string
	withFakePosting(t, nil, func(_ context.Context, _, _ string, _ int, b string) (string, error) { body = b; return b, nil })
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 0, "bot", "", "VERDICT: PASS\n\nfull reasoning here", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "--- Full skeptic output ---") {
		t.Fatalf("expected full output section: %s", body)
	}
	if !strings.Contains(body, "full reasoning here") {
		t.Fatalf("expected full reasoning text: %s", body)
	}
}

func TestPostVerdict_OmitsFullOutputSectionWhenIdenticalToVerdict(t *testing.T) {
	var body string
	withFakePosting(t, nil, func(_ context.Context, _, _ string, _ int, b string) (string, error) { body = b; return b, nil })
	_, err := PostVerdict(ctx, "owner", "repo", 42, "VERDICT: PASS", 0, "bot", "", "VERDICT: PASS", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "--- Full skeptic output ---") {
		t.Fatalf("did not expect a full-output section when llmOutput == verdict: %s", body)
	}
}

func TestIsGhNotFoundError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"HTTP 404: Not Found", true},
		{"resource not found", true},
		{"gh: 404", true},
		{"HTTP 403: Forbidden", false},
		{"network timeout", false},
	}
	for _, c := range cases {
		got := IsGhNotFoundError(errors.New(c.msg))
		if got != c.want {
			t.Errorf("IsGhNotFoundError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestIsGhForbiddenError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"edit conflict is recoverable", "HTTP 403: you must be the author to edit this", true},
		{"not the author phrasing", "403 forbidden: not the author of this comment", true},
		{"rate limit 403 is not recoverable", "403 rate limit exceeded", false},
		{"abuse detection is not recoverable", "403 forbidden: abuse detection triggered", false},
		{"authentication failure is not recoverable", "403 authentication required", false},
		{"invalid token is not recoverable", "403 invalid token provided", false},
		{"resource not accessible is not recoverable", "403 Resource not accessible by integration", false},
		{"plain forbidden with no edit-conflict phrase is not recoverable", "403 Forbidden", false},
		{"non-403 error is not a forbidden error at all", "500 internal server error", false},
		// Discriminating case: a message matching BOTH a non-recoverable
		// pattern (rate limit) AND the edit-conflict pattern (not the
		// author). The non-recoverable check must win — without it, this
		// would incorrectly return true. This is the case that actually
		// exercises the isNonRecoverable override; the two cases above
		// ("rate limit 403"/"abuse detection") don't contain any
		// edit-conflict phrase, so they'd return false even with the
		// override removed — a mutation-test gap caught while hardening
		// this suite (removing the override left them all green).
		{"non-recoverable pattern wins even when edit-conflict phrase is also present", "403 rate limit exceeded: not the author of this comment", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsGhForbiddenError(errors.New(c.msg))
			if got != c.want {
				t.Errorf("IsGhForbiddenError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestIsGhNotFoundError_NilErrorIsFalse(t *testing.T) {
	if IsGhNotFoundError(nil) {
		t.Fatal("expected false for nil error")
	}
}

func TestIsGhForbiddenError_NilErrorIsFalse(t *testing.T) {
	if IsGhForbiddenError(nil) {
		t.Fatal("expected false for nil error")
	}
}
