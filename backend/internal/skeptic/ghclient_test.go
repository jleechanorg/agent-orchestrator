package skeptic

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

func withFakeGH(t *testing.T, run func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	orig := ghRunner
	ghRunner = run
	t.Cleanup(func() { ghRunner = orig })
}

func TestIsRateLimitOrGraphQLError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"API rate limit exceeded", true},
		{"GraphQL: something went wrong", true},
		{"graphql error occurred", true},
		{"HTTP 404: Not Found", false},
		{"connection refused", false},
	}
	for _, c := range cases {
		if got := isRateLimitOrGraphQLError(c.msg); got != c.want {
			t.Errorf("isRateLimitOrGraphQLError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestDecodeGHBase64_StripsEmbeddedNewlines(t *testing.T) {
	want := "hello world, this is test content"
	encoded := base64.StdEncoding.EncodeToString([]byte(want))
	// GitHub wraps base64 at 60 chars with literal newlines.
	wrapped := encoded[:len(encoded)/2] + "\n" + encoded[len(encoded)/2:]
	got, err := decodeGHBase64(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractAllDiffFilePaths(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"diff --git a/b.go b/b.go",
		"--- a/b.go",
		"+++ /dev/null",
		"Binary files a/img.png and b/img.png differ",
	}, "\n")
	got := extractAllDiffFilePaths(diff)
	want := []string{"a.go", "b.go", "img.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestExtractAllDiffFilePaths_BroaderThanGetChangedFiles documents the
// deliberate divergence from prompt.go's getChangedFiles: a bare
// "--- a/x"/"+++ b/x" pair WITHOUT a "diff --git" header IS counted here
// (unlike getChangedFiles, which requires the git header for a plain
// modification) — this matches gh-client.ts's own separate, more
// permissive extraction used specifically for test-file discovery.
func TestExtractAllDiffFilePaths_BroaderThanGetChangedFiles(t *testing.T) {
	diff := "--- a/foo_test.go\n+++ b/foo_test.go"
	got := extractAllDiffFilePaths(diff)
	if len(got) != 1 || got[0] != "foo_test.go" {
		t.Fatalf("got %v, want [foo_test.go] even without a diff --git header", got)
	}
	// Sanity: getChangedFiles (the narrower prompt.go extractor) does NOT
	// count this same input, confirming the two extractors are genuinely
	// different, not accidentally identical.
	if narrow := getChangedFiles(diff); len(narrow) != 0 {
		t.Fatalf("getChangedFiles(%q) = %v, want none (sanity check that the two extractors differ)", diff, narrow)
	}
}

func TestIsTestFilePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"foo.test.ts", true},
		{"foo.spec.ts", true},
		{"pkg/tests/foo.go", true}, // requires a LEADING slash before "test(s)"; a bare "tests/foo.go" does not match
		{"pkg/test/foo.go", true},
		{"__tests__/foo.js", true},
		{"src/main.go", false},
		{"README.md", false},
		// Real, worth-flagging gap (mirrors TS exactly, not a Go bug): NONE
		// of these patterns recognize Go's "_test.go" naming convention —
		// TEST_PATTERNS is JS/TS-project-shaped (.test./.spec./tests//
		// __tests__). If this ported code is ever used to evaluate PRs to
		// a Go repo (like agent-orchestrator itself), it will not detect
		// "foo_test.go" as a test file. Not fixed here — faithfully
		// porting TS behavior, not improving it; flagged as a follow-up.
		{"internal/skeptic/foo_test.go", false},
	}
	for _, c := range cases {
		if got := isTestFilePath(c.path); got != c.want {
			t.Errorf("isTestFilePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestFetchDesignDoc_GitHubAPISuccess(t *testing.T) {
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "api" {
			t.Fatalf("expected api call, got %v", args)
		}
		content := base64.StdEncoding.EncodeToString([]byte("design content"))
		return []byte(`{"content":"` + content + `","encoding":"base64"}`), nil
	})
	got, err := FetchDesignDoc(ctx, "owner", "repo", 42, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != "design content" {
		t.Fatalf("got %v, want design content", got)
	}
}

func TestFetchDesignDoc_404ReturnsNilNotError(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New(`gh: HTTP 404: Not Found (https://api.github.com/repos/o/r/contents/x)`)
	})
	got, err := FetchDesignDoc(ctx, "owner", "repo", 42, "")
	if err != nil {
		t.Fatalf("expected nil error on 404, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil doc on 404, got %v", *got)
	}
}

func TestFetchDesignDoc_OtherErrorIsPropagated(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("connection reset")
	})
	_, err := FetchDesignDoc(ctx, "owner", "repo", 42, "")
	if err == nil {
		t.Fatal("expected a non-nil error for a non-404 failure, not a silent nil")
	}
}

func TestFetchDesignDoc_LocalFallbackWhenOwnerRepoEmpty(t *testing.T) {
	origToplevel := gitRevParseToplevel
	origRead := osReadFile
	tmpDir := t.TempDir()
	gitRevParseToplevel = func(context.Context) (string, error) { return tmpDir, nil }
	osReadFile = os.ReadFile
	t.Cleanup(func() {
		gitRevParseToplevel = origToplevel
		osReadFile = origRead
	})

	docPath := filepath.Join(tmpDir, "docs", "design", "pr-designs")
	if err := os.MkdirAll(docPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docPath, "pr-7.md"), []byte("local doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FetchDesignDoc(ctx, "", "", 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != "local doc" {
		t.Fatalf("got %v, want local doc", got)
	}
}

func TestFetchDesignDoc_LocalFallbackMissingFileReturnsNil(t *testing.T) {
	origToplevel := gitRevParseToplevel
	origRead := osReadFile
	tmpDir := t.TempDir()
	gitRevParseToplevel = func(context.Context) (string, error) { return tmpDir, nil }
	osReadFile = os.ReadFile
	t.Cleanup(func() {
		gitRevParseToplevel = origToplevel
		osReadFile = origRead
	})

	got, err := FetchDesignDoc(ctx, "", "", 999, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil for a missing local file", got)
	}
}

func TestFetchPRMeta_GraphQLSuccess(t *testing.T) {
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"data":{"repository":{"pullRequest":{"number":42,"title":"Fix it","body":"body","state":"OPEN","headRefOid":"abc","baseRefName":"main","isDraft":false}}}}`), nil
	})
	got, err := FetchPRMeta(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 42 || got.Title != "Fix it" || got.HeadRefOID != "abc" {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchPRMeta_GraphQLErrorFallsBackToREST(t *testing.T) {
	calls := 0
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("GraphQL: rate limit exceeded")
		}
		return []byte(`{"number":42,"title":"REST title","body":"b","state":"open","head":{"sha":"deadbeef"},"base":{"ref":"main"},"draft":true}`), nil
	})
	got, err := FetchPRMeta(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "REST title" || got.State != "OPEN" || got.HeadRefOID != "deadbeef" || !got.IsDraft {
		t.Fatalf("got %+v, want REST-sourced fields with uppercased state", got)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (GraphQL attempt + REST fallback)", calls)
	}
}

func TestFetchPRMeta_NonGraphQLErrorNotRetried(t *testing.T) {
	calls := 0
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("some unrelated failure")
	})
	_, err := FetchPRMeta(ctx, "owner", "repo", 42)
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no REST fallback for a non-GraphQL/rate-limit error)", calls)
	}
}

func TestFetchReviews_GraphQLSuccess(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[{"author":{"login":"coderabbitai"},"state":"APPROVED","body":"lgtm","submittedAt":"2026-01-01T00:00:00Z","commit":{"oid":"abc"}}]}}}}}`), nil
	})
	got, err := FetchReviews(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != "approved" || got[0].Author.Login != "coderabbitai" || got[0].CommitID != "abc" {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchReviews_RESTFallbackOmitsNilAuthor(t *testing.T) {
	calls := 0
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("graphql failure")
		}
		return []byte(`[{"user":null,"state":"COMMENTED","body":"hi","submitted_at":"2026-01-01T00:00:00Z","commit_id":"xyz"}]`), nil
	})
	got, err := FetchReviews(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Author != nil {
		t.Fatalf("got %+v, want a nil Author for a null user", got)
	}
}

func TestFetchDiff_SuccessReturnsRawOutput(t *testing.T) {
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("--- a/x\n+++ b/x"), nil
	})
	got := FetchDiff(ctx, "owner", "repo", 42)
	if got != "--- a/x\n+++ b/x" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchDiff_FailureDegradesToPlaceholder(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("network error")
	})
	got := FetchDiff(ctx, "owner", "repo", 42)
	if got != "(diff unavailable)" {
		t.Fatalf("got %q, want the placeholder", got)
	}
}

// Note: fixtures below use "foo.test.ts"-style names (TEST_PATTERNS is
// JS/TS-project-shaped: .test./.spec./tests//__tests__), not Go's
// "_test.go" convention — see the flagged gap in TestIsTestFilePath.

func TestFetchTestFileContents_ExtractsFromDiffAndDecodesBase64(t *testing.T) {
	diff := "diff --git a/foo.test.ts b/foo.test.ts\n--- a/foo.test.ts\n+++ b/foo.test.ts"
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		content := base64.StdEncoding.EncodeToString([]byte("package foo"))
		return []byte(`{"content":"` + content + `","encoding":"base64"}`), nil
	})
	got, err := FetchTestFileContents(ctx, "owner", "repo", 42, diff, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "foo.test.ts" || got[0].Content != "package foo" {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchTestFileContents_FallsBackToNameOnlyWhenDiffHasNoTestFiles(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go" // no test files in diff
	var sawNameOnlyCall bool
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "pr" {
			sawNameOnlyCall = true
			return []byte("main.go\nmain.test.ts\n"), nil
		}
		content := base64.StdEncoding.EncodeToString([]byte("test content"))
		return []byte(`{"content":"` + content + `","encoding":"base64"}`), nil
	})
	got, err := FetchTestFileContents(ctx, "owner", "repo", 42, diff, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sawNameOnlyCall {
		t.Fatal("expected a `gh pr diff --name-only` fallback call since the diff had no test files")
	}
	if len(got) != 1 || got[0].Name != "main.test.ts" {
		t.Fatalf("got %+v, want only main.test.ts", got)
	}
}

func TestFetchTestFileContents_IndividualFileFailureIsNonFatal(t *testing.T) {
	diff := "diff --git a/a.test.ts b/a.test.ts\n--- a/a.test.ts\n+++ b/a.test.ts\ndiff --git a/b.test.ts b/b.test.ts\n--- a/b.test.ts\n+++ b/b.test.ts"
	call := 0
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		call++
		if call == 1 {
			return nil, errors.New("404")
		}
		content := base64.StdEncoding.EncodeToString([]byte("ok"))
		return []byte(`{"content":"` + content + `","encoding":"base64"}`), nil
	})
	got, err := FetchTestFileContents(ctx, "owner", "repo", 42, diff, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "b.test.ts" {
		t.Fatalf("got %+v, want only b.test.ts (a.test.ts's fetch failed and should be skipped, not fatal)", got)
	}
}

func TestFetchTestFileContents_NoTestFilesReturnsNilNotError(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go"
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("main.go\n"), nil // name-only fallback finds no test files either
	})
	got, err := FetchTestFileContents(ctx, "owner", "repo", 42, diff, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestFetchIssueComments_FlattensPages(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`[[{"id":1,"body":"first","user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z"}],[{"id":2,"body":"second","user":{"login":"b"},"created_at":"2026-01-02T00:00:00Z"}]]`), nil
	})
	got, err := FetchIssueComments(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("got %+v, want 2 flattened comments in page order", got)
	}
}

func TestPatchComment_ArgvShape(t *testing.T) {
	var gotArgs []string
	withFakeGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return nil, nil
	})
	if err := PatchComment(ctx, "owner", "repo", 99, "new body"); err != nil {
		t.Fatal(err)
	}
	want := []string{"api", "--method", "PATCH", "repos/owner/repo/issues/comments/99", "--field", "body=new body"}
	if len(gotArgs) != len(want) {
		t.Fatalf("got %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("got %v, want %v", gotArgs, want)
		}
	}
}

func TestCreateComment_ReturnsPostedBody(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) { return nil, nil })
	got, err := CreateComment(ctx, "owner", "repo", 42, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("got %q, want the exact posted body echoed back", got)
	}
}

func TestCreateComment_PropagatesError(t *testing.T) {
	withFakeGH(t, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("boom") })
	_, err := CreateComment(ctx, "owner", "repo", 42, "hello")
	if err == nil {
		t.Fatal("expected an error to propagate")
	}
}
