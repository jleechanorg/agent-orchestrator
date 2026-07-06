package skeptic

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestHarness(t *testing.T) {
	r := New()
	if got := r.Harness(); got != domain.ReviewerSkeptic {
		t.Errorf("Harness() = %q, want %q", got, domain.ReviewerSkeptic)
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantO   string
		wantR   string
		wantN   int
		wantErr bool
	}{
		{"plain", "https://github.com/jleechanorg/agent-orchestrator/pull/737", "jleechanorg", "agent-orchestrator", 737, false},
		{"with trailing slash", "https://github.com/jleechanorg/agent-orchestrator/pull/737/", "jleechanorg", "agent-orchestrator", 737, false},
		{"with query", "https://github.com/o/r/pull/1?diff=split", "o", "r", 1, false},
		{"with fragment", "https://github.com/o/r/pull/1#discussion_r123", "o", "r", 1, false},
		{"http not https", "http://github.com/o/r/pull/1", "o", "r", 1, false},
		{"missing scheme", "github.com/o/r/pull/1", "", "", 0, true},
		{"wrong host", "https://gitlab.com/o/r/pull/1", "", "", 0, true},
		{"missing pull", "https://github.com/o/r/issues/1", "", "", 0, true},
		{"non-numeric", "https://github.com/o/r/pull/abc", "", "", 0, true},
		{"empty", "", "", "", 0, true},
		{"whitespace padded", "  https://github.com/o/r/pull/42  ", "o", "r", 42, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, r2, n, err := parsePRURL(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if o != tc.wantO || r2 != tc.wantR || n != tc.wantN {
				t.Errorf("got (%q,%q,%d), want (%q,%q,%d)", o, r2, n, tc.wantO, tc.wantR, tc.wantN)
			}
		})
	}
}

func TestRequestIDForRun(t *testing.T) {
	inv := ports.ReviewInvocation{
		TargetSHA: "1f60b765a842b1c23fef1eb5d8866fe7b7d489a3",
		RunID:     "run-123",
	}
	got := requestIDForRun(inv)
	want := "gate-1f60b765a842b1c23fef1eb5d8866fe7b7d489a3-run-123-1"
	if got != want {
		t.Errorf("requestIDForRun = %q, want %q", got, want)
	}
}

func TestReviewCommandBuildsArgv(t *testing.T) {
	r := New()
	inv := ports.ReviewInvocation{
		ReviewerID: "review-w1",
		RunID:      "run-abc",
		PRURL:      "https://github.com/jleechanorg/agent-orchestrator/pull/737",
		TargetSHA:  "1f60b765a842b1c23fef1eb5d8866fe7b7d489a3",
	}
	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	want := []string{
		"ao-ts",
		"skeptic",
		"verify",
		"--pr", "737",
		"--repo", "jleechanorg/agent-orchestrator",
		"--trigger-sha", "1f60b765a842b1c23fef1eb5d8866fe7b7d489a3",
		"--request-id", "gate-1f60b765a842b1c23fef1eb5d8866fe7b7d489a3-run-abc-1",
	}
	if len(spec.Argv) != len(want) {
		t.Fatalf("argv length = %d, want %d (%v)", len(spec.Argv), len(want), spec.Argv)
	}
	for i := range want {
		if spec.Argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, spec.Argv[i], want[i])
		}
	}
	if spec.Env != nil {
		t.Errorf("Env should be nil (inherit daemon env), got %v", spec.Env)
	}
}

func TestReviewCommandRejectsBadInputs(t *testing.T) {
	r := New()
	cases := []struct {
		name string
		inv  ports.ReviewInvocation
	}{
		{
			"empty PRURL",
			ports.ReviewInvocation{PRURL: "", TargetSHA: "abc", RunID: "r"},
		},
		{
			"empty SHA",
			ports.ReviewInvocation{PRURL: "https://github.com/o/r/pull/1", TargetSHA: "", RunID: "r"},
		},
		{
			"empty run id",
			ports.ReviewInvocation{PRURL: "https://github.com/o/r/pull/1", TargetSHA: "abc", RunID: ""},
		},
		{
			"non-github URL",
			ports.ReviewInvocation{PRURL: "https://example.com/x", TargetSHA: "abc", RunID: "r"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.ReviewCommand(context.Background(), tc.inv); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestReviewMessageIncludesRunCommand(t *testing.T) {
	r := New()
	inv := ports.ReviewInvocation{
		RunID:     "run-xyz",
		PRURL:     "https://github.com/jleechanorg/agent-orchestrator/pull/737",
		TargetSHA: "1f60b765a842b1c23fef1eb5d8866fe7b7d489a3",
	}
	got, err := r.ReviewMessage(context.Background(), inv)
	if err != nil {
		t.Fatalf("ReviewMessage: %v", err)
	}
	for _, want := range []string{
		"jleechanorg/agent-orchestrator",
		"PR #737",
		"1f60b765a842b1c23fef1eb5d8866fe7b7d489a3",
		"run-xyz",
		"--request-id",
		"gate-1f60b765a842b1c23fef1eb5d8866fe7b7d489a3-run-xyz-1",
		"ao review submit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ReviewMessage missing %q\n-- got --\n%s", want, got)
		}
	}
}

func TestReviewMessageRejectsBadURL(t *testing.T) {
	r := New()
	_, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{
		PRURL:     "not a url",
		TargetSHA: "abc",
		RunID:     "r",
	})
	if err == nil {
		t.Fatal("expected error for non-github URL, got nil")
	}
}