package llmeval

import (
	"context"
	"strings"
	"testing"
)

var ctx = context.Background()

func fakeRunner(result Result) Runner {
	return func(context.Context, string) Result { return result }
}

func TestEval_FirstModelWithValidVerdictWins(t *testing.T) {
	runners := Runners{
		ModelCodex:  fakeRunner(Result{ValidVerdict: true, Output: "VERDICT: PASS"}),
		ModelClaude: fakeRunner(Result{ValidVerdict: true, Output: "VERDICT: FAIL"}),
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude})
	if got != "VERDICT: PASS" {
		t.Fatalf("got %q, want VERDICT: PASS (codex should win, claude never called)", got)
	}
}

func TestEval_FallsThroughUnavailableModel(t *testing.T) {
	calledClaude := false
	runners := Runners{
		ModelCodex: fakeRunner(Result{}), // unavailable: ValidVerdict=false, Err=""
		ModelClaude: func(context.Context, string) Result {
			calledClaude = true
			return Result{ValidVerdict: true, Output: "VERDICT: PASS"}
		},
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude})
	if !calledClaude {
		t.Fatal("claude should have been tried after codex was unavailable")
	}
	if got != "VERDICT: PASS" {
		t.Fatalf("got %q, want VERDICT: PASS", got)
	}
}

func TestEval_FallsThroughMissingVerdictError(t *testing.T) {
	runners := Runners{
		ModelCodex:  fakeRunner(Result{Output: "some text with no verdict", Err: "Codex output missing VERDICT line (got some text...)"}),
		ModelClaude: fakeRunner(Result{ValidVerdict: true, Output: "VERDICT: FAIL"}),
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude})
	if got != "VERDICT: FAIL" {
		t.Fatalf("got %q, want VERDICT: FAIL (claude should have been tried)", got)
	}
}

func TestEval_AllModelsExhausted(t *testing.T) {
	runners := Runners{
		ModelCodex:  fakeRunner(Result{Err: "boom codex"}),
		ModelClaude: fakeRunner(Result{Err: "boom claude"}),
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude})
	if !strings.HasPrefix(got, "VERDICT: FAIL — infra: All LLM tools exhausted.") {
		t.Fatalf("got %q, want the exhausted-chain fail message", got)
	}
	if !strings.Contains(got, "boom claude") {
		t.Fatalf("got %q, want the last model's error in the message", got)
	}
}

// TestEval_StopsEarlyOnRepeatedSameSignature covers llm-eval.ts's dedup
// early-stop: 3 consecutive models producing the same outcome signature
// (here, the same literal error string) is treated as a systemic issue and
// the chain stops rather than trying the remaining models.
func TestEval_StopsEarlyOnRepeatedSameSignature(t *testing.T) {
	calledFourth := false
	runners := Runners{
		ModelCodex:  fakeRunner(Result{Err: "same infra error"}),
		ModelClaude: fakeRunner(Result{Err: "same infra error"}),
		ModelGemini: fakeRunner(Result{Err: "same infra error"}),
		ModelMinimax: func(context.Context, string) Result {
			calledFourth = true
			return Result{ValidVerdict: true, Output: "VERDICT: PASS"}
		},
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude, ModelGemini, ModelMinimax})
	if calledFourth {
		t.Fatal("4th model should never be called — chain must stop early after 3 identical signatures")
	}
	if !strings.Contains(got, "3 consecutive models returned same outcome") {
		t.Fatalf("got %q, want the early-stop dedup message", got)
	}
	if !strings.Contains(got, "same infra error") {
		t.Fatalf("got %q, want the repeated error text surfaced", got)
	}
}

// TestEval_MissingVerdictSignatureNormalizedAcrossModels covers a subtlety
// in llm-eval.ts: two different models each producing "missing VERDICT"
// (with different model names baked into their error strings) must be
// recognized as the SAME signature for dedup purposes, not two different
// ones — otherwise the early-stop would never trigger for this case.
func TestEval_MissingVerdictSignatureNormalizedAcrossModels(t *testing.T) {
	calledFourth := false
	runners := Runners{
		ModelCodex:  fakeRunner(Result{Err: "Codex output missing VERDICT line (got foo...)"}),
		ModelClaude: fakeRunner(Result{Err: "Claude output missing VERDICT line (got bar...)"}),
		ModelGemini: fakeRunner(Result{Err: "Gemini output missing VERDICT line (got baz...)"}),
		ModelMinimax: func(context.Context, string) Result {
			calledFourth = true
			return Result{ValidVerdict: true, Output: "VERDICT: PASS"}
		},
	}
	got := Eval(ctx, "prompt", runners, []Model{ModelCodex, ModelClaude, ModelGemini, ModelMinimax})
	if calledFourth {
		t.Fatal("4th model should never be called — 3 differently-worded missing-VERDICT errors must normalize to one signature")
	}
	if !strings.Contains(got, "missing_verdict") {
		t.Fatalf("got %q, want the normalized missing_verdict signature", got)
	}
}

func TestEval_EmptyChainUsesDefaultChain(t *testing.T) {
	called := map[Model]bool{}
	runners := Runners{}
	for _, m := range DefaultChain {
		mCopy := m
		runners[m] = func(context.Context, string) Result {
			called[mCopy] = true
			return Result{} // unavailable, keep falling through
		}
	}
	Eval(ctx, "prompt", runners, nil)
	for _, m := range DefaultChain {
		if !called[m] {
			t.Fatalf("model %s from DefaultChain was never tried when chain was nil", m)
		}
	}
}

func TestRotateToStart(t *testing.T) {
	got, ok := RotateToStart(ModelGemini)
	if !ok {
		t.Fatal("expected ok=true for a known model")
	}
	want := []Model{ModelGemini, ModelMinimax, ModelAgy, ModelCodex, ModelClaude}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRotateToStart_UnknownModel(t *testing.T) {
	_, ok := RotateToStart(Model("not-a-real-model"))
	if ok {
		t.Fatal("expected ok=false for an unknown model")
	}
}
