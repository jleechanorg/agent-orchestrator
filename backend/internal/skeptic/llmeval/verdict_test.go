package llmeval

import "testing"

func TestStrictVerdictRegex(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain pass", "VERDICT: PASS", true},
		{"plain fail", "VERDICT: FAIL", true},
		{"markdown bold", "**VERDICT: FAIL**", true},
		{"markdown header", "## VERDICT: FAIL", true},
		{"blockquote", "> **VERDICT: PASS**", true},
		{"trailing detail", "VERDICT: PASS — all gates green", true},
		{"em dash detail", "VERDICT: FAIL — evidence missing", true},
		{"skipped is rejected by strict regex", "VERDICT: SKIPPED", false},
		{"no verdict at all", "the evaluation looks fine to me", false},
		{"not line-anchored — prefixed text rejected", "Example: VERDICT: PASS", false},
		// Matches TS buildVerdictLineRe's "im" flags: the whole line
		// (including the verdict word) is matched case-insensitively.
		{"case-insensitive verdict word", "VERDICT: pass", true},
		{"case-insensitive VERDICT keyword", "verdict: PASS", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StrictVerdictRegex.MatchString(c.input)
			if got != c.want {
				t.Fatalf("StrictVerdictRegex.MatchString(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestLineRegex_SkippedAllowedWhenIncluded(t *testing.T) {
	re := LineRegex([]string{"PASS", "FAIL", "SKIPPED"})
	if !re.MatchString("VERDICT: SKIPPED") {
		t.Fatal("expected SKIPPED to match when explicitly included in the verdict set")
	}
}

// TestLineRegex_LastMatchWins mirrors TS skeptic-reviewer.ts's
// lastVerdictIn: when an LLM echoes a prompt template containing an early
// "VERDICT: PASS" example before its real terminal verdict, callers must use
// FindAllString and take the LAST match, not the first. This test documents
// that LineRegex itself is not anchored to "only one line in the whole
// text" — it's a per-line matcher; last-match selection is the caller's
// responsibility (see codex.go's use of FindAllString).
func TestLineRegex_LastMatchWins(t *testing.T) {
	text := "VERDICT: PASS\n\n...\n\nVERDICT: FAIL"
	re := LineRegex([]string{"PASS", "FAIL"})
	matches := re.FindAllString(text, -1)
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2 (one echoed, one real): %v", len(matches), matches)
	}
	last := matches[len(matches)-1]
	if last != "VERDICT: FAIL" {
		t.Fatalf("last match = %q, want VERDICT: FAIL", last)
	}
}
