package skeptic

import "testing"

func TestEscapeRegexLiteral(t *testing.T) {
	// Verify escaped output is literal-safe when embedded in a pattern —
	// a request-id containing regex metacharacters must not be treated as
	// regex syntax.
	re := VerdictLineRegex // sanity: package compiles/regex is valid
	_ = re
	escaped := EscapeRegexLiteral("a.b*c")
	if escaped == "a.b*c" {
		t.Fatal("expected metacharacters to be escaped")
	}
}

func TestVerdictLineRegex_FormatVariants(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain", "VERDICT: PASS", true},
		{"blockquote", "> **VERDICT: SKIPPED**", true},
		{"markdown bold", "**VERDICT: FAIL**", true},
		{"markdown header", "## VERDICT: FAIL", true},
		{"no verdict", "nothing here", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerdictLineRegex.MatchString(c.input); got != c.want {
				t.Fatalf("MatchString(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestHasSkepticRequestId(t *testing.T) {
	body := "<!-- skeptic-request-id-abc123 -->"
	if !HasSkepticRequestId(body, "abc123") {
		t.Fatal("expected match")
	}
	if HasSkepticRequestId(body, "other") {
		t.Fatal("expected no match for a different request id")
	}
	if HasSkepticRequestId(body, "") {
		t.Fatal("expected false for an empty request id")
	}
}

func TestHasSkepticRequestId_EscapesMetacharacters(t *testing.T) {
	// A request id containing regex metacharacters must be treated
	// literally, not as regex syntax.
	body := "<!-- skeptic-request-id-a.b*c -->"
	if !HasSkepticRequestId(body, "a.b*c") {
		t.Fatal("expected literal match")
	}
	if HasSkepticRequestId(body, "aXbYc") {
		t.Fatal("metacharacters must not act as wildcards")
	}
}

func TestHasSkepticHeadSha(t *testing.T) {
	body := "<!-- skeptic-head-sha-deadbeef -->"
	if !HasSkepticHeadSha(body, "deadbeef") {
		t.Fatal("expected match")
	}
	if HasSkepticHeadSha(body, "") {
		t.Fatal("expected false for an empty head sha")
	}
}

func TestHasCompletePassingGateMarkers(t *testing.T) {
	complete := ""
	for _, g := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "8a", "8b", "8c", "8d"} {
		complete += "<!-- skeptic-gate-" + g + ":PASS -->\n"
	}
	if !HasCompletePassingGateMarkers(complete) {
		t.Fatal("expected all markers present to satisfy the contract")
	}
}

func TestHasCompletePassingGateMarkers_MissingPrimaryGate(t *testing.T) {
	body := "<!-- skeptic-gate-1:PASS -->\n<!-- skeptic-gate-2:PASS -->" // missing 3-8
	if HasCompletePassingGateMarkers(body) {
		t.Fatal("expected false when a primary gate marker is missing")
	}
}

func TestHasCompletePassingGateMarkers_MissingSubMarker(t *testing.T) {
	body := ""
	for _, g := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		body += "<!-- skeptic-gate-" + g + ":PASS -->\n"
	}
	body += "<!-- skeptic-gate-8a:PASS -->\n<!-- skeptic-gate-8b:PASS -->\n<!-- skeptic-gate-8c:PASS -->"
	// missing 8d
	if HasCompletePassingGateMarkers(body) {
		t.Fatal("expected false when a gate-8 sub-marker is missing")
	}
}

func TestIsFreshPassVerdictContractSatisfied(t *testing.T) {
	complete := "<!-- skeptic-request-id-req1 -->\n<!-- skeptic-head-sha-abc -->\n"
	for _, g := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "8a", "8b", "8c", "8d"} {
		complete += "<!-- skeptic-gate-" + g + ":PASS -->\n"
	}
	if !IsFreshPassVerdictContractSatisfied(complete, "abc", "req1") {
		t.Fatal("expected the contract to be satisfied")
	}
	if IsFreshPassVerdictContractSatisfied(complete, "different-sha", "req1") {
		t.Fatal("expected false for a mismatched head sha")
	}
}

func TestBindVerdictOutput_NoVerdictLineFailsClosed(t *testing.T) {
	got := BindVerdictOutput("the model said nothing useful")
	if got.VerdictType != "" {
		t.Fatalf("VerdictType = %q, want empty for unparseable output", got.VerdictType)
	}
	if got.VerdictLine != "VERDICT: FAIL — could not parse LLM output (expected VERDICT: PASS/FAIL/SKIPPED)" {
		t.Fatalf("unexpected verdict line: %q", got.VerdictLine)
	}
}

func TestBindVerdictOutput_PlainFailPassesThrough(t *testing.T) {
	got := BindVerdictOutput("some reasoning\nVERDICT: FAIL")
	if got.VerdictType != "FAIL" {
		t.Fatalf("VerdictType = %q, want FAIL", got.VerdictType)
	}
	if got.VerdictLine != "VERDICT: FAIL" {
		t.Fatalf("VerdictLine = %q, want VERDICT: FAIL", got.VerdictLine)
	}
}

// TestBindVerdictOutput_PassDowngradedWithoutCompleteMarkers is the
// fail-closed safety net: a model claiming PASS without a complete gate
// marker table must be downgraded to FAIL.
func TestBindVerdictOutput_PassDowngradedWithoutCompleteMarkers(t *testing.T) {
	got := BindVerdictOutput("looks good\nVERDICT: PASS")
	if got.VerdictType != "FAIL" {
		t.Fatalf("VerdictType = %q, want FAIL (downgraded)", got.VerdictType)
	}
	if got.VerdictLine != "VERDICT: FAIL — PASS missing complete skeptic gate table" {
		t.Fatalf("unexpected verdict line: %q", got.VerdictLine)
	}
}

func TestBindVerdictOutput_PassWithCompleteMarkersIsHonored(t *testing.T) {
	raw := ""
	for _, g := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "8a", "8b", "8c", "8d"} {
		raw += "<!-- skeptic-gate-" + g + ":PASS -->\n"
	}
	raw += "VERDICT: PASS"
	got := BindVerdictOutput(raw)
	if got.VerdictType != "PASS" {
		t.Fatalf("VerdictType = %q, want PASS", got.VerdictType)
	}
	if got.LLMOutput != raw {
		t.Fatal("LLMOutput should pass through unchanged when not downgraded")
	}
}

func TestGetVerdictColor(t *testing.T) {
	cases := map[string]string{"PASS": "green", "SKIPPED": "yellow", "FAIL": "red", "": "red"}
	for verdict, want := range cases {
		if got := GetVerdictColor(verdict); got != want {
			t.Errorf("GetVerdictColor(%q) = %q, want %q", verdict, got, want)
		}
	}
}
