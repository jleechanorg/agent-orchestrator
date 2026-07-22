package skeptic

import "regexp"

// skepticGateMarkerRe matches a single `<!-- skeptic-gate-N:PASS|FAIL|SKIPPED -->`
// marker line. Mirrors the regex literal inline in extractSkepticGateMarkers
// in verdict-utils.ts.
var skepticGateMarkerRe = regexp.MustCompile(`(?i)<!--\s*skeptic-gate-(?:[1-8]|8a|8b|8c|8d)\s*:\s*(?:PASS|FAIL|SKIPPED)\s*-->`)

// ExtractSkepticGateMarkers returns every skeptic-gate-N marker comment
// found in body, in order of occurrence. Mirrors extractSkepticGateMarkers
// in verdict-utils.ts.
func ExtractSkepticGateMarkers(body string) []string {
	return skepticGateMarkerRe.FindAllString(body, -1)
}
