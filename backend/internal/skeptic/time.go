package skeptic

import "time"

// parseRFC3339Lenient parses a GitHub-style timestamp string, tolerating
// the handful of layouts the API actually returns. Mirrors JS's
// `new Date(s).getTime()` leniency more than Go's strict RFC3339 parser
// would alone.
func parseRFC3339Lenient(s string) (int64, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700"}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err != nil {
			lastErr = err
			continue
		}
		return t.UnixMilli(), nil
	}
	return 0, lastErr
}

// nowRFC3339 returns the current UTC time in RFC3339 form, mirroring
// `new Date().toISOString()` in prompt.ts.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
