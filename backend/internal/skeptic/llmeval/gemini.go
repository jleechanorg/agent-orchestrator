package llmeval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// geminiHTTPPost is a package variable so tests can fake the Gemini API
// response without a real network call — mirrors cmdRunner's role for the
// CLI-shelling-out adapters. Returns the HTTP status code, response body,
// and any transport-level error (network failure, timeout, DNS, etc).
var geminiHTTPPost = func(ctx context.Context, url string, body []byte) (status int, respBody []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// TryGemini calls the Gemini API directly over HTTP (not a CLI binary —
// native HTTP avoids interactive-CLI-process hangs), mirroring
// tryGeminiPrint in llm-eval-gemini.ts. Requires GEMINI_API_KEY; model
// defaults to gemini-2.5-flash, overridable via GEMINI_MODEL. Fail-closed:
// a response without a VERDICT line still returns a non-empty Err.
func TryGemini(ctx context.Context, prompt string) Result {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return Result{} // no credentials — fallback chain continues
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)

	reqBody, err := json.Marshal(geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
	})
	if err != nil {
		return Result{Err: fmt.Sprintf("Gemini request encode failed: %s", err)}
	}

	status, respBody, err := geminiHTTPPost(ctx, url, reqBody)
	if err != nil {
		if ctx.Err() != nil {
			return Result{} // timed out / canceled — treat as unavailable, try next
		}
		return Result{Err: fmt.Sprintf("Gemini API call failed: %s", firstLine(err.Error(), 300))}
	}

	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
			return Result{} // shared-credential/rate-limit failure — try next
		}
		return Result{Err: fmt.Sprintf("Gemini API returned status %d: %s", status, truncate(string(respBody), 300))}
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Result{Err: fmt.Sprintf("Gemini API response decode failed: %s", err)}
	}
	var output string
	if len(parsed.Candidates) > 0 && len(parsed.Candidates[0].Content.Parts) > 0 {
		output = strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text)
	}

	if !StrictVerdictRegex.MatchString(output) {
		return Result{Output: output, Err: fmt.Sprintf("Gemini output missing VERDICT line (got %s...)", truncate(output, 100))}
	}
	return Result{ValidVerdict: true, Output: output}
}
