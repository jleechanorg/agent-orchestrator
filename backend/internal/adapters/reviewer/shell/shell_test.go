package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNewWithConfig(t *testing.T) {
	t.Run("empty cmd", func(t *testing.T) {
		_, err := NewWithConfig(ReviewerConfig{Cmd: []string{}})
		if err == nil || !strings.Contains(err.Error(), "cmd is empty") {
			t.Fatalf("expected cmd is empty error, got: %v", err)
		}
	})

	t.Run("unknown placeholder", func(t *testing.T) {
		_, err := NewWithConfig(ReviewerConfig{
			Cmd: []string{"echo", "{invalid_placeholder}"},
		})
		if err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
			t.Fatalf("expected unknown placeholder error, got: %v", err)
		}
	})

	t.Run("valid placeholders", func(t *testing.T) {
		cfg := ReviewerConfig{
			Cmd: []string{"ao-ts", "skeptic", "verify", "--pr", "{pr_number}", "--repo", "{repo}"},
		}
		r, err := NewWithConfig(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Harness() != domain.ReviewerShell {
			t.Errorf("expected harness %v, got %v", domain.ReviewerShell, r.Harness())
		}
	})
}

func TestSentinelReviewer(t *testing.T) {
	r := New()
	if r.Harness() != domain.ReviewerShell {
		t.Errorf("expected harness %v, got %v", domain.ReviewerShell, r.Harness())
	}

	inv := ports.ReviewInvocation{
		PRURL: "https://github.com/owner/repo/pull/42",
	}
	_, err := r.ReviewCommand(context.Background(), inv)
	if err == nil || !strings.Contains(err.Error(), "command is not configured") {
		t.Fatalf("expected unconfigured error for sentinel, got: %v", err)
	}
}

func TestReviewCommand(t *testing.T) {
	cfg := ReviewerConfig{
		Cmd: []string{
			"run-something",
			"--url", "{pr_url}",
			"--sha", "{target_sha}",
			"--run", "{run_id}",
			"--rev", "{reviewer_id}",
			"--ws", "{workspace_path}",
			"--num", "{pr_number}",
			"--repo-name", "{repo}",
		},
		Env: map[string]string{"SOME_ENV": "value"},
	}

	r, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inv := ports.ReviewInvocation{
		ReviewerID:    "reviewer-1",
		RunID:         "run-123",
		PRURL:         "https://github.com/jleechanorg/agent-orchestrator/pull/737",
		TargetSHA:     "sha12345",
		WorkspacePath: "/path/to/ws",
	}

	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatalf("ReviewCommand error: %v", err)
	}

	expectedArgv := []string{
		"run-something",
		"--url", "https://github.com/jleechanorg/agent-orchestrator/pull/737",
		"--sha", "sha12345",
		"--run", "run-123",
		"--rev", "reviewer-1",
		"--ws", "/path/to/ws",
		"--num", "737",
		"--repo-name", "agent-orchestrator",
	}

	if len(spec.Argv) != len(expectedArgv) {
		t.Fatalf("argv length = %d, want %d", len(spec.Argv), len(expectedArgv))
	}
	for i, arg := range spec.Argv {
		if arg != expectedArgv[i] {
			t.Errorf("argv[%d] = %q, want %q", i, arg, expectedArgv[i])
		}
	}

	if spec.Env["SOME_ENV"] != "value" {
		t.Errorf("expected env SOME_ENV to be 'value', got %q", spec.Env["SOME_ENV"])
	}

	// Test ReviewMessage
	msg, err := r.ReviewMessage(context.Background(), inv)
	if err != nil {
		t.Fatalf("ReviewMessage error: %v", err)
	}
	expectedMsg := "(harness=shell) re-run: run-something --url https://github.com/jleechanorg/agent-orchestrator/pull/737 --sha sha12345 --run run-123 --rev reviewer-1 --ws /path/to/ws --num 737 --repo-name agent-orchestrator"
	if msg != expectedMsg {
		t.Errorf("message = %q, want %q", msg, expectedMsg)
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		url     string
		owner   string
		repo    string
		number  string
		wantErr bool
	}{
		{"https://github.com/owner/repo/pull/123", "owner", "repo", "123", false},
		{"https://github.com/owner/repo/pull/123/", "owner", "repo", "123", false},
		{"github.com/owner/repo/pull/123", "owner", "repo", "123", false},
		{"http://github.com/owner/repo/pull/123", "owner", "repo", "123", false},
		{"https://github.com/owner/repo/pull/abc", "", "", "", true},
		{"https://other.com/owner/repo/pull/123", "", "", "", true},
		{"", "", "", "", true},
	}

	for _, tc := range cases {
		o, r, n, err := parsePRURL(tc.url)
		if (err != nil) != tc.wantErr {
			t.Errorf("parsePRURL(%q) err = %v, wantErr = %v", tc.url, err, tc.wantErr)
		}
		if !tc.wantErr {
			if o != tc.owner || r != tc.repo || n != tc.number {
				t.Errorf("parsePRURL(%q) = (%q, %q, %q), want (%q, %q, %q)", tc.url, o, r, n, tc.owner, tc.repo, tc.number)
			}
		}
	}
}
