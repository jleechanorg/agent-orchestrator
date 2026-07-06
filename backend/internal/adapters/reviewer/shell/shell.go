// Package shell is the generic config-driven reviewer adapter. It exists so
// one-off harnesses (skeptic, agy wrapper, custom CLIs) don't need per-tool
// Go adapter code — operators wire them via ReviewerConfig.Cmd with template
// substitution against the standard {pr_url}, {pr_number}, {repo},
// {target_sha}, {run_id}, {reviewer_id}, {workspace_path} placeholders.
package shell

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ReviewerConfig is the operator-supplied wiring for a shell-driven reviewer.
// It is declared here (not in domain) so the shell adapter compiles
// independently of the domain-side ReviewerConfig; the domain agent reconciles
// the two shapes when the upstream type lands.
type ReviewerConfig struct {
	Cmd []string
	Env map[string]string
}

// Reviewer is the generic shell-command reviewer adapter.
type Reviewer struct {
	cmd []string
	env map[string]string
}

// New builds the sentinel shell reviewer adapter with no config.
func New() *Reviewer {
	return &Reviewer{}
}

// NewWithConfig builds a configured shell reviewer adapter. It returns an error
// when the configured command is empty or when any {placeholder} referenced in
// argv is not in the substitution table.
func NewWithConfig(cfg ReviewerConfig) (*Reviewer, error) {
	if len(cfg.Cmd) == 0 {
		return nil, fmt.Errorf("shell reviewer: cmd is empty")
	}
	for _, arg := range cfg.Cmd {
		for _, name := range placeholdersIn(arg) {
			if _, ok := substitutionTable()[name]; !ok {
				return nil, fmt.Errorf("cmd template references unknown placeholder {%s}", name)
			}
		}
	}
	return &Reviewer{cmd: append([]string(nil), cfg.Cmd...), env: cfg.Env}, nil
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerShell
}

// ReviewCommand returns the argv and env to launch the configured command
// for a fresh review pass. The argv is derived by substituting the supported
// placeholders against the invocation context; substitution is single-pass
// (no recursive expansion of values).
func (r *Reviewer) ReviewCommand(_ context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if len(r.cmd) == 0 {
		return ports.ReviewCommandSpec{}, fmt.Errorf("shell reviewer command is not configured")
	}
	values, err := buildSubstitutions(inv)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: substitute(r.cmd, values), Env: r.env}, nil
}

// ReviewMessage returns a re-run hint for an already-running pane — a held-
// open shell can re-invoke the configured command by copy-pasting this line.
// We embed the substituted command so the operator sees exactly what the
// harness would launch, with harness=shell called out for grep-ability.
func (r *Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	spec, err := r.ReviewCommand(ctx, inv)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(harness=shell) re-run: %s", strings.Join(spec.Argv, " ")), nil
}

var _ ports.Reviewer = (*Reviewer)(nil)

// placeholderPattern matches a {name} token where name is a non-empty
// identifier ([A-Za-z0-9_]+). Backslash-escapes (\{ and \}) are not supported
// — the template grammar is intentionally narrow.
var placeholderPattern = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// placeholdersIn returns the set of placeholder names that appear in s,
// preserving the first-seen order for stable error messages.
func placeholdersIn(s string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(s, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// substitutionTable returns the canonical placeholder → value resolver. Each
// entry produces a concrete string when applied to an invocation; an error
// short-circuits the whole command build.
type substitutionFn func(ports.ReviewInvocation) (string, error)

func substitutionTable() map[string]substitutionFn {
	return map[string]substitutionFn{
		"pr_url": func(inv ports.ReviewInvocation) (string, error) {
			return inv.PRURL, nil
		},
		"target_sha": func(inv ports.ReviewInvocation) (string, error) {
			return inv.TargetSHA, nil
		},
		"run_id": func(inv ports.ReviewInvocation) (string, error) {
			return inv.RunID, nil
		},
		"reviewer_id": func(inv ports.ReviewInvocation) (string, error) {
			return inv.ReviewerID, nil
		},
		"workspace_path": func(inv ports.ReviewInvocation) (string, error) {
			return inv.WorkspacePath, nil
		},
		"pr_number": func(inv ports.ReviewInvocation) (string, error) {
			_, _, n, err := parsePRURL(inv.PRURL)
			return n, err
		},
		"repo": func(inv ports.ReviewInvocation) (string, error) {
			_, repo, _, err := parsePRURL(inv.PRURL)
			return repo, err
		},
	}
}

// buildSubstitutions resolves every supported placeholder against inv. The
// cmd is scanned separately (in New) to figure out which placeholders the
// operator wired up; here we resolve the whole table once and let substitute
// pick what it needs. Parsing PRURL is cheap and always safe.
func buildSubstitutions(inv ports.ReviewInvocation) (map[string]string, error) {
	out := make(map[string]string, len(substitutionTable()))
	for name, fn := range substitutionTable() {
		value, err := fn(inv)
		if err != nil {
			return nil, fmt.Errorf("resolve {%s}: %w", name, err)
		}
		out[name] = value
	}
	return out, nil
}

// substitute returns a copy of argv with every {name} token replaced by the
// corresponding entry in values. Unknown placeholders in argv (only reachable
// if a caller bypassed New's validation) are left as-is so the resulting
// command still surfaces the typo to the operator.
func substitute(argv []string, values map[string]string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		out[i] = placeholderPattern.ReplaceAllStringFunc(arg, func(match string) string {
			name := match[1 : len(match)-1]
			if v, ok := values[name]; ok {
				return v
			}
			return match
		})
	}
	return out
}

// parsePRURL extracts owner, repo, and PR number from a GitHub pull-request
// URL of the form https://github.com/<owner>/<repo>/pull/<n>. Anything else
// yields an error; pr_number and {repo} depend on this parse.
func parsePRURL(raw string) (owner, repo, number string, err error) {
	if raw == "" {
		return "", "", "", fmt.Errorf("PRURL is empty")
	}
	// If the URL has no scheme and doesn't start with a slash, prepend https://
	// so that url.Parse correctly identifies the host and path.
	parsedURL := raw
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "/") {
		parsedURL = "https://" + raw
	}
	u, err := url.Parse(parsedURL)
	if err != nil {
		return "", "", "", fmt.Errorf("parse PRURL: %w", err)
	}
	// Accept https://github.com/<owner>/<repo>/pull/<n> (and the bare
	// github.com/<owner>/<repo>/pull/<n> form for tests/dev fixtures).
	host := u.Host
	if host == "" {
		host = "github.com"
	} else if !strings.EqualFold(host, "github.com") && !strings.HasSuffix(strings.ToLower(host), ".github.com") {
		return "", "", "", fmt.Errorf("PRURL host %q is not github.com", host)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", "", fmt.Errorf("PRURL %q is not a /pull/<n> path", raw)
	}
	owner = parts[0]
	repo = parts[1]
	if _, perr := strconv.Atoi(parts[3]); perr != nil {
		return "", "", "", fmt.Errorf("PRURL %q has non-numeric PR id %q", raw, parts[3])
	}
	number = parts[3]
	return owner, repo, number, nil
}
