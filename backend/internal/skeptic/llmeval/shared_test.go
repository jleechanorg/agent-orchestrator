package llmeval

import "testing"

func TestIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"binary not found (Go phrasing)", `exec: "codex": executable file not found in $PATH`, true},
		{"enoent (node-style, defensive)", "spawn codex ENOENT", true},
		{"401", "request failed: 401 Unauthorized", true},
		{"403", "403 Forbidden", true},
		{"429", "429 Too Many Requests", true},
		{"rate limit phrase", "you have hit a rate limit", true},
		{"quota", "insufficient_quota: exceeded", true},
		{"not logged in", "Not logged in · Please run /login", true},
		{"usage limits", "You've hit your usage limits for this billing period", true},
		{"config error", "Error loading config.toml: invalid key", true},
		{"real evaluation failure is NOT unavailable", "panic: nil pointer dereference", false},
		{"missing verdict is NOT unavailable", "Codex output missing VERDICT line (got some text...)", false},
		{"401 substring inside a longer number must not false-positive", "took 4013ms to respond", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsUnavailable(c.msg)
			if got != c.want {
				t.Fatalf("IsUnavailable(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"401", "401 Unauthorized", true},
		{"not logged in", "Not logged in · Please run /login", true},
		{"usage limits shared billing", "usage limits reached", true},
		{"generic crash is not an auth error", "segmentation fault", false},
		{"missing binary is not an auth error", "executable file not found in $PATH", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsAuthError(c.msg)
			if got != c.want {
				t.Fatalf("IsAuthError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestDefaultCodexModel_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("AO_LLM_EVAL_CODEX_MODEL", "")
	if got := DefaultCodexModel(); got != "gpt-5.5" {
		t.Fatalf("got %q, want gpt-5.5 default", got)
	}
}

func TestDefaultCodexModel_HonorsOverride(t *testing.T) {
	t.Setenv("AO_LLM_EVAL_CODEX_MODEL", "custom-model")
	if got := DefaultCodexModel(); got != "custom-model" {
		t.Fatalf("got %q, want custom-model override", got)
	}
}
