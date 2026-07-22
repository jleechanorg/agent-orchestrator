package skeptic

import (
	"bytes"
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// ghRunner shells out to the `gh` CLI and returns stdout, following the
// exec-mocking convention already used elsewhere in this daemon (see
// internal/adapters/agent/authprobe/authprobe.go's CmdRunner and
// internal/skeptic/llmeval/exec.go's cmdRunner) — a swappable package-level
// var so tests don't require a real `gh` binary or network access.
var ghRunner = func(ctx context.Context, args ...string) (stdout []byte, err error) {
	cmd := process.CommandContext(ctx, "gh", args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr == nil {
		return outBuf.Bytes(), nil
	}
	msg := strings.TrimSpace(errBuf.String())
	if msg == "" {
		msg = runErr.Error()
	}
	// Include stdout in the error too — some `gh api` failures still emit a
	// JSON error body on stdout (e.g. {"message":"Not Found",...}), and
	// callers below classify errors by scanning for "404"/"rate limit"
	// text that may only appear there, mirroring TS callers which read
	// err.stdout / err.stderr both.
	if outBuf.Len() > 0 {
		msg = msg + " " + outBuf.String()
	}
	return outBuf.Bytes(), ghError(msg)
}

type ghError string

func (e ghError) Error() string { return string(e) }
