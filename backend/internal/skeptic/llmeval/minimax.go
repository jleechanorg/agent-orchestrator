package llmeval

import (
	"context"
	"fmt"
	"os"
	"time"
)

// DefaultMinimaxBaseURL is MiniMax's Anthropic-compatible API endpoint,
// overridable via MINIMAX_ANTHROPIC_BASE_URL. Mirrors
// DEFAULT_MINIMAX_BASE_URL in llm-eval-shared.ts.
const DefaultMinimaxBaseURL = "https://api.minimax.io/anthropic"

// TryMinimax runs MiniMax via the claude CLI binary with ANTHROPIC_BASE_URL
// (and API key) overridden to MiniMax's Anthropic-compatible endpoint,
// mirroring tryMinimaxPrint in llm-eval-minimax.ts. Requires
// MINIMAX_API_KEY. Same binary-resolution simplification as TryClaude
// (reuses claudecode.ResolveClaudeBinary instead of re-walking a candidate
// list) and the same 429-retry-once-after-2s behavior as TryClaude.
func TryMinimax(ctx context.Context, prompt string) Result {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		return Result{} // no credentials — fallback chain continues
	}
	baseURL := os.Getenv("MINIMAX_ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = DefaultMinimaxBaseURL
	}

	binary, err := resolveClaudeBinary(ctx)
	if err != nil {
		return Result{}
	}

	envOverrides := map[string]string{
		"ANTHROPIC_API_KEY":    apiKey,
		"ANTHROPIC_AUTH_TOKEN": apiKey,
		"ANTHROPIC_BASE_URL":   baseURL,
	}
	args := []string{"--bare", "--dangerously-skip-permissions", "--print"}

	out, runErr := cmdRunnerEnv(ctx, binary, args, prompt, envOverrides)
	if runErr != nil && isRateLimited(runErr.Error()) {
		if sleepErr := claudeSleep(ctx, 2*time.Second); sleepErr != nil {
			return Result{Err: firstLine(sleepErr.Error())}
		}
		out, runErr = cmdRunnerEnv(ctx, binary, args, prompt, envOverrides)
	}
	if runErr != nil {
		if IsUnavailable(runErr.Error()) {
			return Result{}
		}
		return Result{Err: firstLine(runErr.Error())}
	}

	if !StrictVerdictRegex.MatchString(out) {
		return Result{Output: out, Err: fmt.Sprintf("minimax output missing VERDICT line (got %s...)", truncate(out, 100))}
	}
	return Result{ValidVerdict: true, Output: out}
}
