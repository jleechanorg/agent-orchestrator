package llmeval

import (
	"context"
	"fmt"

	codexagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
)

// resolveCodexBinary is a package variable so tests can inject a fake path
// without depending on a real codex install. Production wiring resolves the
// same binary the codex worker-agent adapter uses (cross-platform PATH +
// well-known-install-location search), so both code paths agree on which
// codex the daemon considers "the" codex.
var resolveCodexBinary = codexagent.ResolveCodexBinary

// TryCodex runs `codex exec --model <model> -c check_for_update_on_startup=false -`
// with prompt piped to stdin, mirroring tryCodexPrint in llm-eval-codex.ts:
// prompt goes over stdin (never argv) to avoid leaking it into `ps` output
// and to sidestep OS argument-length limits. Fail-closed: a codex run that
// completes but produces no VERDICT line still returns a non-empty Err (the
// chain treats "ran, no verdict" differently from "unavailable, try next" —
// see Result's doc comment).
func TryCodex(ctx context.Context, prompt string) Result {
	binary, err := resolveCodexBinary(ctx)
	if err != nil {
		return Result{}
	}

	model := DefaultCodexModel()
	out, runErr := cmdRunner(ctx, binary, []string{"exec", "--model", model, "-c", "check_for_update_on_startup=false", "-"}, prompt)
	if runErr != nil {
		if IsUnavailable(runErr.Error()) {
			return Result{}
		}
		// Retry without the -c flag — older codex releases may reject it,
		// mirroring llm-eval-codex.ts's fallback attempt.
		out, runErr = cmdRunner(ctx, binary, []string{"exec", "--model", model, "-"}, prompt)
		if runErr != nil {
			if IsUnavailable(runErr.Error()) {
				return Result{}
			}
			return Result{Err: firstLine(runErr.Error(), 300)}
		}
	}

	if !StrictVerdictRegex.MatchString(out) {
		return Result{Output: out, Err: fmt.Sprintf("Codex output missing VERDICT line (got %s...)", truncate(out, 100))}
	}
	return Result{ValidVerdict: true, Output: out}
}
