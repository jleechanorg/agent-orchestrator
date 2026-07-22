package llmeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agyagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agy"
)

// resolveAgyBinary is a package variable so tests can inject a fake path —
// same reuse-existing-resolver approach as TryCodex/TryClaude.
var resolveAgyBinary = agyagent.ResolveAgyBinary

// ensureAgyTrustedFolders is a package variable (default: the real
// filesystem write below) so tests don't touch the real $HOME. Mirrors
// tryAgyPrint's pre-run side effect in llm-eval-agy.ts: agy (Google
// Antigravity/Gemini CLI) refuses to run non-interactively in an untrusted
// folder, so /tmp, /private/tmp, and the resolved temp dir are marked
// trusted in ~/.gemini/trustedFolders.json before every headless call.
// Best-effort and silent on failure — an unwritable/corrupt config must
// never block the eval chain, and an existing file that fails to parse is
// left untouched rather than clobbered (same skip-on-parse-failure
// behavior as the TS source, to avoid erasing unrelated trust entries).
var ensureAgyTrustedFolders = func() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	trustedFoldersPath := filepath.Join(home, ".gemini", "trustedFolders.json")
	if err := os.MkdirAll(filepath.Dir(trustedFoldersPath), 0o750); err != nil {
		return
	}

	trusted := map[string]string{}
	shouldWrite := true
	if raw, err := os.ReadFile(trustedFoldersPath); err == nil {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			var parsed map[string]string
			if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
				trusted = parsed
			} else {
				shouldWrite = false // parse failure — don't clobber the existing file
			}
		}
	}

	for _, p := range []string{"/tmp", "/private/tmp", os.TempDir()} {
		if p == "" {
			continue
		}
		trusted[p] = "TRUST_FOLDER"
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			trusted[resolved] = "TRUST_FOLDER"
		}
	}

	if !shouldWrite {
		return
	}
	encoded, err := json.MarshalIndent(trusted, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(trustedFoldersPath, encoded, 0o600)
}

// TryAgy runs agy (Google Antigravity/Gemini CLI) for headless evaluation,
// mirroring tryAgyPrint in llm-eval-agy.ts. The permission flag depends on
// the resolved binary's name: a "gemini"-named binary takes --yolo, any
// other agy binary takes --dangerously-skip-permissions. Fail-closed: a run
// that completes but produces no VERDICT line still returns a non-empty
// Err.
func TryAgy(ctx context.Context, prompt string) Result {
	ensureAgyTrustedFolders()

	binary, err := resolveAgyBinary(ctx)
	if err != nil {
		return Result{}
	}

	permissionFlag := "--dangerously-skip-permissions"
	if strings.Contains(filepath.Base(binary), "gemini") {
		permissionFlag = "--yolo"
	}

	// Run from /tmp — the one path ensureAgyTrustedFolders always marks
	// trusted, mirroring llm-eval-agy.ts's `cwd: "/tmp"` on its
	// execFileSync call. agy's non-interactive workspace-trust check keys
	// off cwd, so writing the trust file alone (without also running FROM
	// a trusted directory) leaves agy refusing to run whenever the
	// daemon's own ambient working directory isn't itself trusted.
	out, runErr := cmdRunnerDir(ctx, binary, []string{permissionFlag, "-p", ""}, prompt, "/tmp")
	if runErr != nil {
		if IsUnavailable(runErr.Error()) {
			return Result{}
		}
		return Result{Err: firstLine(runErr.Error())}
	}

	if !StrictVerdictRegex.MatchString(out) {
		return Result{Output: out, Err: fmt.Sprintf("agy output missing VERDICT line (got %s...)", truncate(out, 100))}
	}
	return Result{ValidVerdict: true, Output: out}
}
