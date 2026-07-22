package llmeval

import (
	"context"
	"fmt"
	"strings"
	"time"

	claudecode "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
)

// resolveClaudeBinary is a package variable so tests can inject a fake path.
//
// Deliberate simplification vs. llm-eval-claude.ts: TS's tryClaudePrint
// iterates its own hardcoded CLAUDE_BINARY_CANDIDATES list, trying each in
// turn (skipping ENOENT, short-circuiting on a shared-credential auth
// error, recording the first infra error otherwise). Go's
// claudecode.ResolveClaudeBinary already performs an equivalent
// multi-candidate search (internal/adapters/agent/binaryutil.ResolveBinary)
// used by the daemon's own claude-code worker-agent adapter, so this reuses
// that single resolved path rather than re-implementing a second,
// independently-drifting candidate list. The executability/EACCES
// candidate-skip nuance from the TS version is not reproduced (tracked as
// a follow-up in bead jleechan-xpz7) — a not-executable resolved binary
// surfaces as a normal exec error instead of silently trying a sibling
// candidate.
var resolveClaudeBinary = claudecode.ResolveClaudeBinary

func isRateLimited(msg string) bool {
	lower := strings.ToLower(msg)
	return reWord429.MatchString(msg) || strings.Contains(lower, "rate_limit") || strings.Contains(lower, "rate limit")
}

// claudeSleep is a package variable so tests don't have to wait out the real
// 2s retry delay.
var claudeSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryClaude runs `claude --bare --dangerously-skip-permissions --print` with
// prompt piped to stdin, mirroring tryClaudePrint in llm-eval-claude.ts
// (modulo the candidate-list simplification documented on
// resolveClaudeBinary above). A 429/rate-limit failure is retried once
// after a 2s pause, mirroring execClaudeBinaryWithRetry in
// llm-eval-shared.ts. Fail-closed: output without a VERDICT line still
// returns a non-empty Err.
func TryClaude(ctx context.Context, prompt string) Result {
	binary, err := resolveClaudeBinary(ctx)
	if err != nil {
		return Result{}
	}

	args := []string{"--bare", "--dangerously-skip-permissions", "--print"}
	out, runErr := cmdRunner(ctx, binary, args, prompt)
	if runErr != nil && isRateLimited(runErr.Error()) {
		if sleepErr := claudeSleep(ctx, 2*time.Second); sleepErr != nil {
			return Result{Err: firstLine(sleepErr.Error())}
		}
		out, runErr = cmdRunner(ctx, binary, args, prompt)
	}
	if runErr != nil {
		if IsUnavailable(runErr.Error()) {
			return Result{}
		}
		return Result{Err: firstLine(runErr.Error())}
	}

	if !StrictVerdictRegex.MatchString(out) {
		return Result{Output: out, Err: fmt.Sprintf("Claude output missing VERDICT line (got %s...)", truncate(out, 100))}
	}
	return Result{ValidVerdict: true, Output: out}
}
