package llmeval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withFakeAgy(t *testing.T, binaryPath string, binaryErr error, run func(ctx context.Context, binary string, args []string, stdin string) (string, error)) {
	t.Helper()
	origResolve := resolveAgyBinary
	origRunner := cmdRunner
	origTrust := ensureAgyTrustedFolders
	resolveAgyBinary = func(context.Context) (string, error) {
		if binaryErr != nil {
			return "", binaryErr
		}
		return binaryPath, nil
	}
	cmdRunner = run
	ensureAgyTrustedFolders = func() {} // skip the real filesystem side effect in these tests
	t.Cleanup(func() {
		resolveAgyBinary = origResolve
		cmdRunner = origRunner
		ensureAgyTrustedFolders = origTrust
	})
}

func TestTryAgy_BinaryUnresolved(t *testing.T) {
	withFakeAgy(t, "", errors.New("not found"), nil)
	got := TryAgy(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
}

func TestTryAgy_DangerouslySkipPermissionsForNonGeminiBinary(t *testing.T) {
	var gotArgs []string
	withFakeAgy(t, "/fake/bin/agy", nil, func(_ context.Context, _ string, args []string, _ string) (string, error) {
		gotArgs = args
		return "VERDICT: PASS", nil
	})
	got := TryAgy(ctx, "prompt")
	if !got.ValidVerdict {
		t.Fatalf("got %+v, want ValidVerdict=true", got)
	}
	if len(gotArgs) == 0 || gotArgs[0] != "--dangerously-skip-permissions" {
		t.Fatalf("args = %v, want first arg --dangerously-skip-permissions for a non-gemini binary name", gotArgs)
	}
}

// TestTryAgy_YoloFlagForGeminiNamedBinary covers the binary-name-dependent
// permission flag from llm-eval-agy.ts: a resolved binary whose basename
// contains "gemini" must use --yolo instead of
// --dangerously-skip-permissions.
func TestTryAgy_YoloFlagForGeminiNamedBinary(t *testing.T) {
	var gotArgs []string
	withFakeAgy(t, "/fake/bin/gemini", nil, func(_ context.Context, _ string, args []string, _ string) (string, error) {
		gotArgs = args
		return "VERDICT: PASS", nil
	})
	TryAgy(ctx, "prompt")
	if len(gotArgs) == 0 || gotArgs[0] != "--yolo" {
		t.Fatalf("args = %v, want first arg --yolo for a gemini-named binary", gotArgs)
	}
}

func TestTryAgy_UnavailableError(t *testing.T) {
	withFakeAgy(t, "/fake/bin/agy", nil, func(context.Context, string, []string, string) (string, error) {
		return "", errors.New("exec: \"agy\": executable file not found in $PATH")
	})
	got := TryAgy(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result", got)
	}
}

func TestTryAgy_SuccessButMissingVerdict(t *testing.T) {
	withFakeAgy(t, "/fake/bin/agy", nil, func(context.Context, string, []string, string) (string, error) {
		return "looks fine", nil
	})
	got := TryAgy(ctx, "prompt")
	if got.ValidVerdict {
		t.Fatal("expected ValidVerdict=false")
	}
	if got.Err == "" {
		t.Fatal("expected a non-empty Err")
	}
}

// TestEnsureAgyTrustedFolders_WritesTrustEntries exercises the real
// filesystem side effect (against a temp HOME, never the real one) —
// confirms /tmp and os.TempDir() end up marked trusted.
func TestEnsureAgyTrustedFolders_WritesTrustEntries(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	ensureAgyTrustedFolders()

	path := filepath.Join(tmpHome, ".gemini", "trustedFolders.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected trustedFolders.json to be written, got err: %s", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("trustedFolders.json is not valid JSON: %s", err)
	}
	if parsed["/tmp"] != "TRUST_FOLDER" {
		t.Fatalf("parsed = %v, want /tmp marked TRUST_FOLDER", parsed)
	}
}

// TestEnsureAgyTrustedFolders_DoesNotClobberUnparsableExisting mirrors the
// TS source's explicit "skip the write if we fail to parse an existing
// file" safeguard — overwriting could erase unrelated trust entries.
func TestEnsureAgyTrustedFolders_DoesNotClobberUnparsableExisting(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	dir := filepath.Join(tmpHome, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trustedFolders.json")
	original := "not valid json{{{"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureAgyTrustedFolders()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("file was modified despite being unparsable: got %q, want untouched %q", raw, original)
	}
}
