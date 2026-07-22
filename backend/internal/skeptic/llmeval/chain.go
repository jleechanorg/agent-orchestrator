package llmeval

import (
	"context"
	"fmt"
	"strings"
)

// Model identifies one of the headless CLI evaluators in the fallback chain.
type Model string

// The five headless evaluators, in DefaultChain order. Mirrors ChainModel in
// llm-eval.ts. "cursor" is deliberately not modeled: TS accepts it for CLI
// compatibility only and immediately rewrites it to "codex" (cursor-agent
// blocks on Workspace Trust in headless mode) — callers translating a
// --model flag should apply that same rewrite before building a chain.
const (
	ModelCodex   Model = "codex"
	ModelClaude  Model = "claude"
	ModelGemini  Model = "gemini"
	ModelMinimax Model = "minimax"
	ModelAgy     Model = "agy"
)

// DefaultChain is the order Eval tries models in when no explicit chain is
// given. Mirrors DEFAULT_CHAIN in llm-eval.ts.
var DefaultChain = []Model{ModelCodex, ModelClaude, ModelGemini, ModelMinimax, ModelAgy}

// Runner runs one model's headless evaluation of prompt.
type Runner func(ctx context.Context, prompt string) Result

// Runners maps each chain model to its Runner. Production code uses
// DefaultRunners (wired to the real per-model CLI adapters); tests inject
// fakes — mirroring this repo's newManager()-style dependency injection
// (see internal/session_manager/manager_test.go) rather than a purpose-built
// exec-mocking framework.
type Runners map[Model]Runner

// dedupThreshold: if this many consecutive models produce the same outcome
// signature, Eval stops early rather than trying the rest of the chain —
// strong evidence of a systemic issue (broken prompt template, expired
// shared credential) where continuing would only waste latency. Mirrors
// DEDUP_THRESHOLD in llm-eval.ts.
const dedupThreshold = 3

// RotateToStart returns DefaultChain rotated so preferred is tried first,
// then the rest of DefaultChain in its normal order (wrapping around) —
// e.g. RotateToStart(ModelGemini) with DefaultChain
// [codex,claude,gemini,minimax,agy] yields [gemini,minimax,agy,codex,claude].
// This mirrors llm-eval.ts's single-string `model` handling: a preferred
// model tries first, but the rest of the chain still runs as fallback
// rather than being an ONLY-this-model restriction. ok=false when preferred
// is not a known chain model.
func RotateToStart(preferred Model) (chain []Model, ok bool) {
	idx := -1
	for i, m := range DefaultChain {
		if m == preferred {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, false
	}
	rotated := make([]Model, 0, len(DefaultChain))
	rotated = append(rotated, DefaultChain[idx:]...)
	rotated = append(rotated, DefaultChain[:idx]...)
	return rotated, true
}

// Eval runs a skeptic-style headless LLM evaluation and returns the raw
// output. It tries models in chain order (DefaultChain when chain is
// empty); the first to produce a valid VERDICT line wins. Any error
// (missing binary, auth failure, rate limit, missing VERDICT line) falls
// through to the next model — mirrors llmEval's chain semantics in
// llm-eval.ts (see that file's doc comment for the full rationale).
//
// If dedupThreshold consecutive models produce the same outcome signature,
// the chain stops early instead of exhausting the rest of the list.
func Eval(ctx context.Context, prompt string, runners Runners, chain []Model) string {
	ordered := chain
	if len(ordered) == 0 {
		ordered = DefaultChain
	}

	var lastError string
	var lastSig string
	sameSigCount := 0

	for _, model := range ordered {
		runner, known := runners[model]
		if !known {
			continue
		}
		result := runner(ctx, prompt)
		if result.ValidVerdict {
			return result.Output
		}

		sig := fmt.Sprintf("unavailable:%s", model)
		if result.Err != "" {
			if strings.Contains(strings.ToLower(result.Err), "missing verdict") {
				sig = "missing_verdict"
			} else {
				sig = "error:" + result.Err
			}
		}

		if sig == lastSig {
			sameSigCount++
		} else {
			sameSigCount = 1
			lastSig = sig
		}

		if sameSigCount >= dedupThreshold {
			lastErrOrSig := lastError
			if lastErrOrSig == "" {
				lastErrOrSig = sig
			}
			return fmt.Sprintf(
				"VERDICT: FAIL — infra: %d consecutive models returned same outcome (%s). Stopped early. Tried: %s. Last error: %s",
				sameSigCount, sig, joinModels(ordered), lastErrOrSig,
			)
		}

		if result.Err != "" {
			lastError = result.Err
		} else if lastError == "" {
			lastError = fmt.Sprintf("%s: not available", model)
		}
	}

	return fmt.Sprintf("VERDICT: FAIL — infra: All LLM tools exhausted. Tried: %s. Last error: %s", joinModels(ordered), lastError)
}

func joinModels(models []Model) string {
	parts := make([]string, len(models))
	for i, m := range models {
		parts[i] = string(m)
	}
	return strings.Join(parts, " → ")
}
