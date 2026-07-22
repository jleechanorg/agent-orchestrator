package llmeval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func withFakeMinimax(t *testing.T, apiKey string, binaryErr error, run func(ctx context.Context, binary string, args []string, stdin string, env map[string]string) (string, error)) {
	t.Helper()
	t.Setenv("MINIMAX_API_KEY", apiKey)
	origResolve := resolveClaudeBinary
	origRunner := cmdRunnerEnv
	origSleep := claudeSleep
	resolveClaudeBinary = func(context.Context) (string, error) {
		if binaryErr != nil {
			return "", binaryErr
		}
		return "/fake/bin/claude", nil
	}
	cmdRunnerEnv = run
	claudeSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() {
		resolveClaudeBinary = origResolve
		cmdRunnerEnv = origRunner
		claudeSleep = origSleep
	})
}

func TestTryMinimax_NoAPIKey(t *testing.T) {
	withFakeMinimax(t, "", nil, nil)
	got := TryMinimax(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" || got.Output != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result when MINIMAX_API_KEY is unset", got)
	}
}

func TestTryMinimax_BinaryUnresolved(t *testing.T) {
	withFakeMinimax(t, "test-key", errors.New("not found"), nil)
	got := TryMinimax(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
}

// TestTryMinimax_OverridesAnthropicEnvToPointAtMinimax is the core
// behavior that distinguishes minimax from plain claude: the same claude
// binary must be invoked with ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN/
// ANTHROPIC_BASE_URL overridden to MiniMax's credentials/endpoint.
func TestTryMinimax_OverridesAnthropicEnvToPointAtMinimax(t *testing.T) {
	var gotEnv map[string]string
	withFakeMinimax(t, "mm-key", nil, func(_ context.Context, _ string, _ []string, _ string, env map[string]string) (string, error) {
		gotEnv = env
		return "VERDICT: PASS", nil
	})
	got := TryMinimax(ctx, "prompt")
	if !got.ValidVerdict {
		t.Fatalf("got %+v, want ValidVerdict=true", got)
	}
	if gotEnv["ANTHROPIC_API_KEY"] != "mm-key" || gotEnv["ANTHROPIC_AUTH_TOKEN"] != "mm-key" {
		t.Fatalf("env = %v, want ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN = mm-key", gotEnv)
	}
	if gotEnv["ANTHROPIC_BASE_URL"] != DefaultMinimaxBaseURL {
		t.Fatalf("env[ANTHROPIC_BASE_URL] = %q, want default %q", gotEnv["ANTHROPIC_BASE_URL"], DefaultMinimaxBaseURL)
	}
}

func TestTryMinimax_HonorsBaseURLOverride(t *testing.T) {
	t.Setenv("MINIMAX_ANTHROPIC_BASE_URL", "https://custom.example/anthropic")
	var gotEnv map[string]string
	withFakeMinimax(t, "mm-key", nil, func(_ context.Context, _ string, _ []string, _ string, env map[string]string) (string, error) {
		gotEnv = env
		return "VERDICT: PASS", nil
	})
	TryMinimax(ctx, "prompt")
	if gotEnv["ANTHROPIC_BASE_URL"] != "https://custom.example/anthropic" {
		t.Fatalf("env[ANTHROPIC_BASE_URL] = %q, want the override", gotEnv["ANTHROPIC_BASE_URL"])
	}
}

func TestTryMinimax_RetriesOnceOn429(t *testing.T) {
	calls := 0
	withFakeMinimax(t, "mm-key", nil, func(context.Context, string, []string, string, map[string]string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("429 Too Many Requests")
		}
		return "VERDICT: FAIL", nil
	})
	got := TryMinimax(ctx, "prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: FAIL" {
		t.Fatalf("got %+v, want the retry to succeed", got)
	}
	if calls != 2 {
		t.Fatalf("cmdRunnerEnv called %d times, want 2", calls)
	}
}

func TestTryMinimax_SuccessButMissingVerdict(t *testing.T) {
	withFakeMinimax(t, "mm-key", nil, func(context.Context, string, []string, string, map[string]string) (string, error) {
		return "looks fine", nil
	})
	got := TryMinimax(ctx, "prompt")
	if got.ValidVerdict {
		t.Fatal("expected ValidVerdict=false")
	}
	if got.Err == "" {
		t.Fatal("expected a non-empty Err")
	}
}
