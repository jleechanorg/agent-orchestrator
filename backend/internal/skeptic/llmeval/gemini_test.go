package llmeval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func withFakeGemini(t *testing.T, apiKey string, post func(ctx context.Context, url string, body []byte) (int, []byte, error)) {
	t.Helper()
	t.Setenv("GEMINI_API_KEY", apiKey)
	origPost := geminiHTTPPost
	geminiHTTPPost = post
	t.Cleanup(func() { geminiHTTPPost = origPost })
}

func geminiOKBody(t *testing.T, text string) []byte {
	t.Helper()
	body, err := json.Marshal(geminiResponse{
		Candidates: []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}{
			{
				Content: struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				}{
					Parts: []struct {
						Text string `json:"text"`
					}{{Text: text}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %s", err)
	}
	return body
}

func TestTryGemini_NoAPIKey(t *testing.T) {
	withFakeGemini(t, "", nil)
	got := TryGemini(ctx, "prompt")
	if got.ValidVerdict || got.Err != "" || got.Output != "" {
		t.Fatalf("got %+v, want a zero-value unavailable Result when GEMINI_API_KEY is unset", got)
	}
}

func TestTryGemini_SuccessWithValidVerdict(t *testing.T) {
	var gotURL string
	withFakeGemini(t, "test-key", func(_ context.Context, url string, _ []byte) (int, []byte, error) {
		gotURL = url
		return http.StatusOK, geminiOKBody(t, "VERDICT: PASS"), nil
	})
	got := TryGemini(ctx, "prompt")
	if !got.ValidVerdict || got.Output != "VERDICT: PASS" {
		t.Fatalf("got %+v, want ValidVerdict=true", got)
	}
	if gotURL == "" || !strings.Contains(gotURL, "test-key") {
		t.Fatalf("url = %q, want it to embed the API key", gotURL)
	}
}

// TestTryGemini_401403429TreatedAsUnavailable covers the shared-credential
// early-return: these three status codes must NOT surface as a hard error —
// the chain should try the next model.
func TestTryGemini_401403429TreatedAsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		withFakeGemini(t, "test-key", func(context.Context, string, []byte) (int, []byte, error) {
			return status, []byte("denied"), nil
		})
		got := TryGemini(ctx, "prompt")
		if got.ValidVerdict || got.Err != "" {
			t.Fatalf("status %d: got %+v, want a zero-value unavailable Result", status, got)
		}
	}
}

func TestTryGemini_OtherErrorStatusIsFailClosed(t *testing.T) {
	withFakeGemini(t, "test-key", func(context.Context, string, []byte) (int, []byte, error) {
		return http.StatusInternalServerError, []byte("server exploded"), nil
	})
	got := TryGemini(ctx, "prompt")
	if got.ValidVerdict || got.Err == "" {
		t.Fatalf("got %+v, want a fail-closed Result with a non-empty Err for a 500", got)
	}
}

func TestTryGemini_TransportErrorIsFailClosed(t *testing.T) {
	withFakeGemini(t, "test-key", func(context.Context, string, []byte) (int, []byte, error) {
		return 0, nil, errors.New("connection reset by peer")
	})
	got := TryGemini(ctx, "prompt")
	if got.ValidVerdict || got.Err == "" {
		t.Fatalf("got %+v, want a fail-closed Result for a real transport error", got)
	}
}

func TestTryGemini_SuccessButMissingVerdict(t *testing.T) {
	withFakeGemini(t, "test-key", func(context.Context, string, []byte) (int, []byte, error) {
		return http.StatusOK, geminiOKBody(t, "looks fine to me"), nil
	})
	got := TryGemini(ctx, "prompt")
	if got.ValidVerdict {
		t.Fatal("expected ValidVerdict=false")
	}
	if got.Err == "" {
		t.Fatal("expected a non-empty Err describing the missing VERDICT line")
	}
}
