package llmeval

import (
	"bytes"
	"context"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// cmdRunner runs a headless CLI evaluator: binary is invoked with args,
// stdin is piped to it, and stdout is returned trimmed. On a non-zero exit
// the returned error's message is stderr (falling back to the raw exec
// error when stderr is empty) — this mirrors the per-model adapters in
// agent-orchestrator-ts, which read the thrown error's message for
// classification (isUnavailable/isAuthError) rather than a raw exit code.
//
// It is a package variable, following the exec-mocking convention already
// established in this repo (see
// internal/adapters/agent/authprobe/authprobe.go's CmdRunner) — tests swap
// it out rather than requiring a real CLI binary on PATH.
var cmdRunner = func(ctx context.Context, binary string, args []string, stdin string) (stdout string, err error) {
	return runWithEnv(ctx, binary, args, stdin, nil)
}

// cmdRunnerEnv is cmdRunner plus environment variable overrides layered on
// top of the current process environment — used by adapters (minimax) that
// invoke a shared CLI binary against an alternate provider endpoint/API key
// rather than the binary's default credentials. A separate package var
// (rather than changing cmdRunner's signature) keeps the simpler adapters'
// existing fakes untouched.
var cmdRunnerEnv = func(ctx context.Context, binary string, args []string, stdin string, envOverrides map[string]string) (stdout string, err error) {
	return runWithEnv(ctx, binary, args, stdin, envOverrides)
}

func runWithEnv(ctx context.Context, binary string, args []string, stdin string, envOverrides map[string]string) (stdout string, err error) {
	cmd := process.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	if envOverrides != nil {
		env := os.Environ()
		for k, v := range envOverrides {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr == nil {
		return strings.TrimSpace(outBuf.String()), nil
	}
	msg := strings.TrimSpace(errBuf.String())
	if msg == "" {
		msg = runErr.Error()
	}
	return strings.TrimSpace(outBuf.String()), errString(msg)
}

type execError string

func (e execError) Error() string { return string(e) }

func errString(msg string) error { return execError(msg) }

// truncate returns s capped at n runes, without splitting a multi-byte rune.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// firstLine returns the first line of s, capped at n runes.
func firstLine(s string, n int) string {
	if idx := strings.IndexByte(s, '\n'); idx != -1 {
		s = s[:idx]
	}
	return truncate(s, n)
}
