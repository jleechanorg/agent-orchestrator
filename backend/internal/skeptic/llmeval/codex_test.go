package llmeval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// withFakeCodex swaps resolveCodexBinary and cmdRunner for the duration of
// the test, restoring the real ones afterward.
func withFakeCodex(t *testing.T, binaryErr error, run func(ctx context.Context, binary string, args []string, stdin string) (string, error)) {
	t.Helper()
	origResolve := resolveCodexBinary
	origRunner := cmdRunner
	resolveCodexBinary = func(context.Context) (string, error) {
		if binaryErr != nil {
			return "", binaryErr
		}
		return "/fake/bin/codex", nil
	}
	cmdRunner = run
	t.Cleanup(func() {
		resolveCodexBinary = origResolve
		cmdRunner = origRunner
	})
}

func TestTryCodex_BinaryUnresolved(t *testing.T) {
	withFakeCodex(t, errors.New("codex: not found"), nil)
	got := TryCodex(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" || got.Output != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
}

func TestTryCodex_SuccessWithValidVerdict(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	withFakeCodex(t, nil, func(_ context.Context, binary string, args []string, stdin string) (string, error) {
		gotArgs = args
		gotStdin = stdin
		return "VERDICT: PASS", nil
	})
	got := TryCodex(ctx, "the prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: PASS" {
		t.Fatalf("got %+v, want ValidVerdict=true Output=VERDICT: PASS", got)
	}
	if gotStdin != "the prompt" {
		t.Fatalf("stdin = %q, want the prompt to be piped via stdin (never argv)", gotStdin)
	}
	wantArgs := []string{"exec", "--model", DefaultCodexModel(), "-c", "check_for_update_on_startup=false", "-"}
	if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestTryCodex_FirstCallUnavailableNoRetry(t *testing.T) {
	calls := 0
	withFakeCodex(t, nil, func(context.Context, string, []string, string) (string, error) {
		calls++
		return "", errors.New("exec: \"codex\": executable file not found in $PATH")
	})
	got := TryCodex(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
	if calls != 1 {
		t.Fatalf("cmdRunner called %d times, want 1 (no retry on unavailable)", calls)
	}
}

// TestTryCodex_RetriesWithoutConfigFlagOnNonUnavailableError covers the
// llm-eval-codex.ts fallback: a non-unavailable first-attempt failure (e.g.
// an older codex release rejecting -c check_for_update_on_startup=false)
// must retry WITHOUT that flag before giving up.
func TestTryCodex_RetriesWithoutConfigFlagOnNonUnavailableError(t *testing.T) {
	var seenArgs [][]string
	withFakeCodex(t, nil, func(_ context.Context, _ string, args []string, _ string) (string, error) {
		seenArgs = append(seenArgs, args)
		if len(seenArgs) == 1 {
			return "", errors.New("unknown flag: -c")
		}
		return "VERDICT: FAIL", nil
	})
	got := TryCodex(ctx, "prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: FAIL" {
		t.Fatalf("got %+v, want the retry's VERDICT: FAIL to succeed", got)
	}
	if len(seenArgs) != 2 {
		t.Fatalf("cmdRunner called %d times, want 2 (initial + retry)", len(seenArgs))
	}
	for _, a := range seenArgs[1] {
		if a == "-c" {
			t.Fatalf("retry args %v still contain -c; retry must drop the config flag", seenArgs[1])
		}
	}
}

func TestTryCodex_BothAttemptsFailNonUnavailable(t *testing.T) {
	calls := 0
	withFakeCodex(t, nil, func(context.Context, string, []string, string) (string, error) {
		calls++
		return "", errors.New("a real crash message that is definitely not an availability error")
	})
	got := TryCodex(ctx, "prompt")
	if got.ValidVerdict || got.Err == "" {
		t.Fatalf("got %+v, want a fail-closed Result with a non-empty Err", got)
	}
	if calls != 2 {
		t.Fatalf("cmdRunner called %d times, want 2 (initial + retry, both failing)", calls)
	}
}

func TestTryCodex_SuccessButMissingVerdict(t *testing.T) {
	withFakeCodex(t, nil, func(context.Context, string, []string, string) (string, error) {
		return "I looked at the diff and it seems fine.", nil
	})
	got := TryCodex(ctx, "prompt")
	if got.ValidVerdict {
		t.Fatal("expected ValidVerdict=false when output has no VERDICT line")
	}
	if !strings.Contains(got.Err, "missing VERDICT line") {
		t.Fatalf("Err = %q, want it to mention the missing VERDICT line", got.Err)
	}
	if got.Output == "" {
		t.Fatal("Output should still carry the raw text even without a valid verdict")
	}
}
