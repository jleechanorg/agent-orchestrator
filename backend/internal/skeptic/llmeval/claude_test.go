package llmeval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func withFakeClaude(t *testing.T, binaryErr error, run func(ctx context.Context, binary string, args []string, stdin string) (string, error)) {
	t.Helper()
	origResolve := resolveClaudeBinary
	origRunner := cmdRunner
	origSleep := claudeSleep
	resolveClaudeBinary = func(context.Context) (string, error) {
		if binaryErr != nil {
			return "", binaryErr
		}
		return "/fake/bin/claude", nil
	}
	cmdRunner = run
	claudeSleep = func(context.Context, time.Duration) error { return nil } // no real sleep in tests
	t.Cleanup(func() {
		resolveClaudeBinary = origResolve
		cmdRunner = origRunner
		claudeSleep = origSleep
	})
}

func TestTryClaude_BinaryUnresolved(t *testing.T) {
	withFakeClaude(t, errors.New("claude: not found"), nil)
	got := TryClaude(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" || got.Output != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
}

func TestTryClaude_SuccessWithValidVerdict(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	withFakeClaude(t, nil, func(_ context.Context, _ string, args []string, stdin string) (string, error) {
		gotArgs = args
		gotStdin = stdin
		return "VERDICT: PASS", nil
	})
	got := TryClaude(ctx, "the prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: PASS" {
		t.Fatalf("got %+v, want ValidVerdict=true", got)
	}
	if gotStdin != "the prompt" {
		t.Fatalf("stdin = %q, want prompt piped via stdin", gotStdin)
	}
	want := []string{"--bare", "--dangerously-skip-permissions", "--print"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v", gotArgs, want)
		}
	}
}

// TestTryClaude_RetriesOnceOn429 covers execClaudeBinaryWithRetry's
// rate-limit retry: a 429 on the first attempt must be retried once after a
// pause, and a second-attempt success must be honored.
func TestTryClaude_RetriesOnceOn429(t *testing.T) {
	calls := 0
	withFakeClaude(t, nil, func(context.Context, string, []string, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("429 Too Many Requests")
		}
		return "VERDICT: FAIL", nil
	})
	got := TryClaude(ctx, "prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: FAIL" {
		t.Fatalf("got %+v, want the retry to succeed with VERDICT: FAIL", got)
	}
	if calls != 2 {
		t.Fatalf("cmdRunner called %d times, want 2 (initial 429 + retry)", calls)
	}
}

func TestTryClaude_NoRetryOnNonRateLimitError(t *testing.T) {
	calls := 0
	withFakeClaude(t, nil, func(context.Context, string, []string, string) (string, error) {
		calls++
		return "", errors.New("a real crash, not rate-limited")
	})
	got := TryClaude(ctx, "prompt")
	if got.ValidVerdict || got.Err == "" {
		t.Fatalf("got %+v, want a fail-closed Result", got)
	}
	if calls != 1 {
		t.Fatalf("cmdRunner called %d times, want 1 (no retry for a non-429 error)", calls)
	}
}

func TestTryClaude_UnavailableAfterRetryExhausted(t *testing.T) {
	withFakeClaude(t, nil, func(context.Context, string, []string, string) (string, error) {
		return "", errors.New("Not logged in · Please run /login")
	})
	got := TryClaude(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result for an auth failure", got)
	}
}

func TestTryClaude_SuccessButMissingVerdict(t *testing.T) {
	withFakeClaude(t, nil, func(context.Context, string, []string, string) (string, error) {
		return "looks fine to me", nil
	})
	got := TryClaude(ctx, "prompt")
	if got.ValidVerdict {
		t.Fatal("expected ValidVerdict=false")
	}
	if got.Err == "" {
		t.Fatal("expected a non-empty Err describing the missing VERDICT line")
	}
}
