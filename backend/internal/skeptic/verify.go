package skeptic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/skeptic/llmeval"
)

// ValidSkepticModels are the models --model accepts. Mirrors
// VALID_SKEPTIC_MODELS (SUPPORTED_MODELS) in modelRunner.ts.
var ValidSkepticModels = []string{"codex", "claude", "gemini", "minimax", "agy"}

// triggerShaFormatRe mirrors the inline /^[0-9a-f]{7,40}$/i literal used to
// validate --trigger-sha in skeptic.ts.
var triggerShaFormatRe = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}$`)

// NormalizeTriggerSHA trims raw and returns it only if it looks like a git
// SHA; otherwise returns "" (mirrors skeptic.ts's inline triggerSha
// normalization, applied identically in both the CLI action and
// findExistingVerdict).
func NormalizeTriggerSHA(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" && triggerShaFormatRe.MatchString(trimmed) {
		return trimmed
	}
	return ""
}

// ResolveRepo determines the owner/repo to operate on: repoFlag if given
// (must be "owner/repo"), else the current directory's repo via `gh repo
// view`. Mirrors resolveRepo in skeptic.ts.
func ResolveRepo(ctx context.Context, repoFlag string) (owner, repo string, err error) {
	if repoFlag != "" {
		parts := strings.Split(repoFlag, "/")
		if len(parts) != 2 {
			return "", "", fmt.Errorf("repo must be in format: owner/repo")
		}
		return parts[0], parts[1], nil
	}
	out, err := ghRunner(ctx, "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", "", fmt.Errorf("could not determine repo. Use --repo owner/repo: %w", err)
	}
	var info struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if jsonErr := json.Unmarshal(out, &info); jsonErr != nil {
		return "", "", fmt.Errorf("could not determine repo. Use --repo owner/repo: %w", jsonErr)
	}
	return info.Owner.Login, info.Name, nil
}

// ExistingVerdict is a previously-posted skeptic verdict comment found on a
// PR, returned by FindExistingVerdict.
type ExistingVerdict struct {
	Verdict   string
	CommentID int
}

var skepticVerdictMarkerRe = regexp.MustCompile(`(?i)<!-- skeptic-agent-verdict -->`)

// FindExistingVerdict scans a PR's issue comments for a prior
// skeptic-agent-verdict comment matching triggerSHA/requestID (when given),
// so the caller can update it idempotently instead of posting a duplicate.
// Mirrors findExistingVerdict in skeptic.ts.
func FindExistingVerdict(ctx context.Context, owner, repo string, prNumber int, triggerSHA, requestID string) (*ExistingVerdict, error) {
	validSHA := NormalizeTriggerSHA(triggerSHA)

	comments, err := FetchIssueComments(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}
	for _, c := range comments {
		if !skepticVerdictMarkerRe.MatchString(c.Body) {
			continue
		}
		if validSHA != "" {
			shaMarkerRe := regexp.MustCompile(`<!-- skeptic-(?:gate|cron)-trigger-` + EscapeRegexLiteral(validSHA) + ` -->`)
			if !shaMarkerRe.MatchString(c.Body) {
				continue
			}
		}
		if requestID != "" {
			ridMarkerRe := regexp.MustCompile(`(?i)<!-- skeptic-request-id-` + EscapeRegexLiteral(requestID) + ` -->`)
			if !ridMarkerRe.MatchString(c.Body) {
				continue
			}
		}
		m := VerdictLineRegex.FindStringSubmatch(c.Body)
		if m != nil {
			return &ExistingVerdict{Verdict: strings.ToUpper(m[1]), CommentID: c.ID}, nil
		}
	}
	return nil, nil
}

// AllFilesExcluded reports whether every file touched in diff matches at
// least one of excludePatterns. An empty diff or empty pattern list always
// returns false (never skip). Mirrors allFilesExcluded in skeptic.ts.
func AllFilesExcluded(diff string, excludePatterns []string) bool {
	if len(excludePatterns) == 0 {
		return false
	}
	files := extractAllDiffFilePaths(diff)
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		matched := false
		for _, p := range excludePatterns {
			if globMatch(f, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

var skepticGatePassMarkerRe = regexp.MustCompile(`(?i)<!--\s*skeptic-gate-([1-8]|8[abcd])\s*:\s*PASS\s*-->`)

// ApplyEvidenceOverride enforces Rule 10: if prBody's ## Evidence section is
// not authentic and verdict claims PASS, force it to FAIL (rewriting any
// PASS gate markers to FAIL too, so the body stays internally consistent).
// Mirrors applyEvidenceOverride in skeptic.ts.
func ApplyEvidenceOverride(verdict, prBody string) string {
	if IsEvidenceAuthentic(prBody) {
		return verdict
	}
	m := VerdictLineRegex.FindStringSubmatch(verdict)
	if m == nil || strings.ToUpper(m[1]) != "PASS" {
		return verdict
	}
	overridden := replaceFirstMatch(verdict, VerdictLineRegex,
		"VERDICT: FAIL — evidence authenticity check failed (Rule 10: missing or fabricated ## Evidence section)")
	overridden = skepticGatePassMarkerRe.ReplaceAllString(overridden, "<!-- skeptic-gate-$1:FAIL -->")
	return overridden
}

// replaceFirstMatch replaces only the first match of re in s — Go's
// regexp.ReplaceAllString has no non-global variant, and skeptic.ts's
// verdict.replace(VERDICT_LINE_RE, ...) call uses a non-global regex so it
// only ever replaces the (expected to be singular) VERDICT line, not every
// occurrence.
func replaceFirstMatch(s string, re *regexp.Regexp, replacement string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + replacement + s[loc[1]:]
}

// RunEvaluation runs the skeptic LLM fallback chain against prompt. If
// model is "" the DefaultChain order is used; otherwise the chain is
// rotated so model is tried first (RotateToStart). Mirrors
// runSkepticEvaluation in modelRunner.ts, minus its unused
// string-or-string-array overload — the real CLI --model flag only ever
// supplies a single value, so that generality is not ported (ponytail:
// single-string ceiling; extend to []string if a caller needs it).
func RunEvaluation(ctx context.Context, runners llmeval.Runners, prompt string, model string) (string, error) {
	chain := llmeval.DefaultChain
	if model != "" {
		valid := false
		for _, m := range ValidSkepticModels {
			if m == model {
				valid = true
				break
			}
		}
		if !valid {
			return "", fmt.Errorf("unsupported skeptic model: %q. Supported models are: %s", model, strings.Join(ValidSkepticModels, ", "))
		}
		rotated, ok := llmeval.RotateToStart(llmeval.Model(model))
		if !ok {
			return "", fmt.Errorf("unsupported skeptic model: %q. Supported models are: %s", model, strings.Join(ValidSkepticModels, ", "))
		}
		chain = rotated
	}
	return llmeval.Eval(ctx, prompt, runners, chain), nil
}
