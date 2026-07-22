package skeptic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// ghJSON shells out to `gh api <endpoint> <args...>` and returns the raw
// JSON response body. Mirrors ghJson in gh-client.ts.
func ghJSON(ctx context.Context, endpoint string, args ...string) ([]byte, error) {
	full := append([]string{"api", endpoint}, args...)
	return ghRunner(ctx, full...)
}

// ghJSONPaginate is ghJSON with --paginate --slurp (REST only) — each page
// becomes one array element in the raw response, which callers flatten.
// Mirrors ghJsonPaginate in gh-client.ts.
func ghJSONPaginate(ctx context.Context, endpoint string, args ...string) ([]byte, error) {
	full := append([]string{"api", "--paginate", "--slurp", endpoint}, args...)
	return ghRunner(ctx, full...)
}

// isRateLimitOrGraphQLError reports whether errMsg indicates a GraphQL
// failure the caller should retry against the REST API. Mirrors the
// repeated `errMsg.includes("rate limit") || errMsg.includes("GraphQL") ||
// errMsg.includes("graphql")` check duplicated across fetchPRMeta and
// fetchReviews in gh-client.ts.
func isRateLimitOrGraphQLError(errMsg string) bool {
	return strings.Contains(errMsg, "rate limit") ||
		strings.Contains(errMsg, "GraphQL") ||
		strings.Contains(errMsg, "graphql")
}

// FetchDesignDoc fetches the design doc for a PR via the GitHub API,
// falling back to a local filesystem read when owner/repo are both empty
// (mirrors the null-owner/repo unit-test fallback path in
// fetchDesignDoc in gh-client.ts). A 404 (doc not yet written) returns
// nil, nil — other API errors are returned so the caller decides whether
// to skip or abort, rather than silently falling back to a local checkout
// that could read the wrong repo's content.
func FetchDesignDoc(ctx context.Context, owner, repo string, prNumber int, ref string) (*string, error) {
	docPath := fmt.Sprintf("docs/design/pr-designs/pr-%d.md", prNumber)

	if owner != "" && repo != "" {
		endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, docPath)
		if ref != "" {
			endpoint += "?ref=" + url.QueryEscape(ref)
		}
		raw, err := ghJSON(ctx, endpoint)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, `"status": "404"`) || strings.Contains(msg, "HTTP 404") {
				return nil, nil
			}
			return nil, err
		}
		var data struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if jsonErr := json.Unmarshal(raw, &data); jsonErr != nil {
			return nil, fmt.Errorf("decode design doc response: %w", jsonErr)
		}
		if data.Content != "" && data.Encoding == "base64" {
			decoded, decErr := decodeGHBase64(data.Content)
			if decErr != nil {
				return nil, fmt.Errorf("decode design doc content: %w", decErr)
			}
			return &decoded, nil
		}
		return nil, nil
	}

	// Fallback: local checkout (only reached when owner/repo are both empty).
	return fetchDesignDocFromLocalCheckout(ctx, docPath)
}

// fetchDesignDocFromLocalCheckout is split out from FetchDesignDoc so tests
// can exercise the local-fallback path (owner/repo empty) without a real
// git checkout, by swapping gitRevParseToplevel and osReadFile.
func fetchDesignDocFromLocalCheckout(ctx context.Context, docPath string) (*string, error) {
	root, err := gitRevParseToplevel(ctx)
	if err != nil {
		return nil, err
	}
	content, err := osReadFile(filepath.Join(root, docPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s := string(content)
	return &s, nil
}

var gitRevParseToplevel = func(ctx context.Context) (string, error) {
	out, err := process.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var osReadFile = os.ReadFile

// decodeGHBase64 decodes a GitHub API base64 content field, which may embed
// literal newlines (GitHub wraps base64 content at 60 chars) — strip them
// before decoding, mirroring content.replace(/\n/g, "") in gh-client.ts.
func decodeGHBase64(content string) (string, error) {
	stripped := strings.ReplaceAll(content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(stripped)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// FetchPRMeta fetches PR metadata via GraphQL, falling back to REST on a
// rate-limit/GraphQL error. Mirrors fetchPRMeta in gh-client.ts.
func FetchPRMeta(ctx context.Context, owner, repo string, prNumber int) (PRInfo, error) {
	query := fmt.Sprintf(
		"{\n  repository(owner:%q, name:%q) {\n    pullRequest(number:%d) {\n      number title body state headRefOid baseRefName isDraft\n    }\n  }\n}",
		owner, repo, prNumber,
	)
	raw, err := ghJSON(ctx, "graphql", "-f", "query="+query)
	if err == nil {
		var resp struct {
			Data struct {
				Repository struct {
					PullRequest *struct {
						Number      int    `json:"number"`
						Title       string `json:"title"`
						Body        string `json:"body"`
						State       string `json:"state"`
						HeadRefOID  string `json:"headRefOid"`
						BaseRefName string `json:"baseRefName"`
						IsDraft     bool   `json:"isDraft"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if jsonErr := json.Unmarshal(raw, &resp); jsonErr != nil {
			return PRInfo{}, fmt.Errorf("decode PR meta response: %w", jsonErr)
		}
		pr := resp.Data.Repository.PullRequest
		if pr == nil {
			return PRInfo{}, fmt.Errorf("PR not found")
		}
		return PRInfo{
			Number:      pr.Number,
			Title:       pr.Title,
			Body:        pr.Body,
			State:       pr.State,
			HeadRefOID:  pr.HeadRefOID,
			BaseRefName: pr.BaseRefName,
			IsDraft:     pr.IsDraft,
		}, nil
	}

	if !isRateLimitOrGraphQLError(err.Error()) {
		return PRInfo{}, err
	}

	// REST fallback.
	raw, restErr := ghJSON(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, prNumber))
	if restErr != nil {
		return PRInfo{}, restErr
	}
	var rest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Draft bool `json:"draft"`
	}
	if jsonErr := json.Unmarshal(raw, &rest); jsonErr != nil {
		return PRInfo{}, fmt.Errorf("decode PR meta REST response: %w", jsonErr)
	}
	return PRInfo{
		Number:      rest.Number,
		Title:       rest.Title,
		Body:        rest.Body,
		State:       strings.ToUpper(rest.State),
		HeadRefOID:  rest.Head.SHA,
		BaseRefName: rest.Base.Ref,
		IsDraft:     rest.Draft,
	}, nil
}

// FetchReviews fetches PR reviews via GraphQL, falling back to REST on a
// rate-limit/GraphQL error. Mirrors fetchReviews in gh-client.ts.
func FetchReviews(ctx context.Context, owner, repo string, prNumber int) ([]ReviewInfo, error) {
	query := fmt.Sprintf(
		"{\n  repository(owner:%q, name:%q) {\n    pullRequest(number:%d) {\n      reviewDecision\n      reviews(last:20) {\n        nodes { author { login } state body submittedAt commit { oid } }\n      }\n    }\n  }\n}",
		owner, repo, prNumber,
	)
	raw, err := ghJSON(ctx, "graphql", "-f", "query="+query)
	if err == nil {
		var resp struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						Reviews struct {
							Nodes []struct {
								Author *struct {
									Login string `json:"login"`
								} `json:"author"`
								State       string `json:"state"`
								Body        string `json:"body"`
								SubmittedAt string `json:"submittedAt"`
								Commit      *struct {
									OID string `json:"oid"`
								} `json:"commit"`
							} `json:"nodes"`
						} `json:"reviews"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if jsonErr := json.Unmarshal(raw, &resp); jsonErr != nil {
			return nil, fmt.Errorf("decode reviews response: %w", jsonErr)
		}
		nodes := resp.Data.Repository.PullRequest.Reviews.Nodes
		out := make([]ReviewInfo, len(nodes))
		for i, n := range nodes {
			r := ReviewInfo{
				State:       strings.ToLower(n.State),
				Body:        n.Body,
				SubmittedAt: n.SubmittedAt,
			}
			if n.Author != nil {
				r.Author = &ReviewAuthor{Login: n.Author.Login}
			}
			if n.Commit != nil {
				r.CommitID = n.Commit.OID
			}
			out[i] = r
		}
		return out, nil
	}

	if !isRateLimitOrGraphQLError(err.Error()) {
		return nil, err
	}

	// REST fallback.
	raw, restErr := ghJSON(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber))
	if restErr != nil {
		return nil, restErr
	}
	var rest []struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submitted_at"`
		CommitID    string `json:"commit_id"`
	}
	if jsonErr := json.Unmarshal(raw, &rest); jsonErr != nil {
		return nil, fmt.Errorf("decode reviews REST response: %w", jsonErr)
	}
	out := make([]ReviewInfo, len(rest))
	for i, r := range rest {
		ri := ReviewInfo{
			State:       strings.ToLower(r.State),
			Body:        r.Body,
			SubmittedAt: r.SubmittedAt,
			CommitID:    r.CommitID,
		}
		if r.User != nil && r.User.Login != "" {
			ri.Author = &ReviewAuthor{Login: r.User.Login}
		}
		out[i] = ri
	}
	return out, nil
}

// FetchDiff fetches the unified diff for a PR. Never returns an error —
// mirrors fetchDiff in gh-client.ts, which degrades to a placeholder string
// on any failure rather than aborting the whole skeptic run over a diff
// fetch hiccup.
func FetchDiff(ctx context.Context, owner, repo string, prNumber int) string {
	out, err := ghRunner(ctx, "pr", "diff", "--repo", owner+"/"+repo, fmt.Sprintf("%d", prNumber))
	if err != nil {
		return "(diff unavailable)"
	}
	return string(out)
}

var testFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.test\.`),
	regexp.MustCompile(`\.spec\.`),
	regexp.MustCompile(`/tests?/`),
	regexp.MustCompile(`__tests?__`),
	regexp.MustCompile(`/test/`),
}

func isTestFilePath(path string) bool {
	for _, re := range testFilePatterns {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// FetchTestFileContents fetches the content of test files changed in a PR,
// extracted from the unified diff (falling back to `gh pr diff
// --name-only` when diff parsing finds no test files). Only files matching
// a test-file naming pattern are fetched. Mirrors fetchTestFileContents in
// gh-client.ts, including its deterministic-insertion-order guarantee
// (settle all fetches first, then insert in testPaths order) — the Go port
// achieves the same ordering with a simple sequential loop instead of
// TS's Promise.allSettled-then-reorder, since Go's ghJSON calls here are
// already sequential.
func FetchTestFileContents(ctx context.Context, owner, repo string, prNumber int, diff, ref string) ([]TestFileContent, error) {
	filePaths := extractAllDiffFilePaths(diff)
	var testPaths []string
	seen := map[string]bool{}
	for _, p := range filePaths {
		if isTestFilePath(p) && !seen[p] {
			seen[p] = true
			testPaths = append(testPaths, p)
		}
	}

	if len(testPaths) == 0 {
		out, err := ghRunner(ctx, "pr", "diff", "--name-only", "--repo", owner+"/"+repo, fmt.Sprintf("%d", prNumber))
		if err == nil {
			testPaths = nil
			seen = map[string]bool{}
			for _, line := range strings.Split(string(out), "\n") {
				p := strings.TrimSpace(line)
				if p != "" && isTestFilePath(p) && !seen[p] {
					seen[p] = true
					testPaths = append(testPaths, p)
				}
			}
		}
		// gh unavailable or network error — degrade gracefully, matching TS's
		// empty-catch (testPaths stays whatever it already was, i.e. empty).
	}

	if len(testPaths) == 0 {
		return nil, nil
	}

	refSuffix := ""
	if ref != "" {
		refSuffix = "?ref=" + url.QueryEscape(ref)
	}
	var results []TestFileContent
	for _, filePath := range testPaths {
		endpoint := fmt.Sprintf("repos/%s/%s/contents/%s%s", owner, repo, filePath, refSuffix)
		raw, err := ghJSON(ctx, endpoint)
		if err != nil {
			continue // individual file fetch failure is non-fatal — skip this file
		}
		var data struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if jsonErr := json.Unmarshal(raw, &data); jsonErr != nil {
			continue
		}
		if data.Content != "" && data.Encoding == "base64" {
			decoded, decErr := decodeGHBase64(data.Content)
			if decErr != nil {
				continue
			}
			results = append(results, TestFileContent{Name: filePath, Content: decoded})
		}
	}
	return results, nil
}

var (
	diffPlusMinusHeaderRe = regexp.MustCompile(`^[+-]{3}[ \t][ab]/(.+)$`)
)

// extractAllDiffFilePaths extracts file paths from a unified diff using
// fetchTestFileContents's OWN inline pattern in gh-client.ts — deliberately
// broader than prompt.go's getChangedFiles: it matches any "---"/"+++"
// file-header line directly (not just a "diff --git" header or a deletion
// pairing), plus "diff --git" headers and binary-file lines. This is a
// distinct extraction rule from getChangedFiles (which only exists to
// match buildSkepticPrompt's own file-count display exactly) — TS keeps
// these two extractors separate rather than sharing one, and this port
// does the same rather than silently unifying them.
func extractAllDiffFilePaths(diff string) []string {
	seen := map[string]bool{}
	var files []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		if m := diffPlusMinusHeaderRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := diffGitHeaderRe.FindStringSubmatch(line); m != nil {
			add(m[2])
		}
		if m := diffBinaryRe.FindStringSubmatch(line); m != nil {
			add(m[2])
		}
	}
	return files
}

// FetchIssueComments fetches every PR/issue comment, paginating through all
// pages. Mirrors fetchIssueComments in gh-client.ts, with one deliberate,
// disclosed improvement: TS's version does an unchecked `as
// Array<IssueComment[]>` cast straight onto the raw REST JSON with no field
// remapping, so its camelCase `createdAt` never actually gets populated at
// runtime (GitHub's real REST API returns snake_case `created_at` — this is
// a latent bug in the TS source, invisible to tsc since `as` casts aren't
// checked). This Go port explicitly decodes the snake_case field and
// copies it into IssueComment.CreatedAt, so CreatedAt IS correctly
// populated here — a real, intentional divergence from what the TS code
// actually does at runtime, found by adversarial review. Not reverted to
// match the TS bug: no known caller in this pipeline currently reads
// CreatedAt, and there is no reason to faithfully reproduce a bug instead
// of just being correct.
func FetchIssueComments(ctx context.Context, owner, repo string, prNumber int) ([]IssueComment, error) {
	raw, err := ghJSONPaginate(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, prNumber))
	if err != nil {
		return nil, err
	}
	var pages [][]struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt   string `json:"created_at"`
		IsMinimized bool   `json:"isMinimized"`
	}
	if jsonErr := json.Unmarshal(raw, &pages); jsonErr != nil {
		return nil, fmt.Errorf("decode issue comments response: %w", jsonErr)
	}
	var out []IssueComment
	for _, page := range pages {
		for _, c := range page {
			out = append(out, IssueComment{
				ID:          c.ID,
				Body:        c.Body,
				User:        ReviewAuthor{Login: c.User.Login},
				CreatedAt:   c.CreatedAt,
				IsMinimized: c.IsMinimized,
			})
		}
	}
	return out, nil
}

// PatchComment updates an existing PR/issue comment's body. Mirrors
// patchComment in gh-client.ts.
func PatchComment(ctx context.Context, owner, repo string, commentID int, body string) error {
	_, err := ghRunner(ctx,
		"api", "--method", "PATCH",
		fmt.Sprintf("repos/%s/%s/issues/comments/%d", owner, repo, commentID),
		"--field", "body="+body,
	)
	return err
}

// CreateComment posts a new PR/issue comment and returns the body that was
// posted. Mirrors createComment in gh-client.ts.
func CreateComment(ctx context.Context, owner, repo string, prNumber int, body string) (string, error) {
	_, err := ghRunner(ctx,
		"api", fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, prNumber),
		"--field", "body="+body,
	)
	if err != nil {
		return "", err
	}
	return body, nil
}
