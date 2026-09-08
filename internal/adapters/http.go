package adapters

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxBodyBytes caps how much of an upstream response we will read. Status
// payloads are a few hundred KB at most; the cap keeps one misbehaving vendor
// from exhausting memory across 200+ concurrent polls.
const maxBodyBytes = 8 << 20 // 8 MiB

// httpResponse is the outcome of a status-endpoint request.
type httpResponse struct {
	Body        []byte
	StatusCode  int
	ContentType string
}

// httpGet performs a GET against a status endpoint and returns the decoded body
// along with the real HTTP status code.
//
// It deliberately does NOT set Accept-Encoding: doing so opts out of net/http's
// transparent gzip handling, which previously left callers decoding compressed
// bytes as JSON whenever a server answered with deflate.
func httpGet(ctx context.Context, client *http.Client, userAgent, endpoint, accept string) (httpResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return httpResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := client.Do(req)
	if err != nil {
		return httpResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	out := httpResponse{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
	}

	reader := io.Reader(io.LimitReader(resp.Body, maxBodyBytes))
	// net/http only decompresses automatically when it set Accept-Encoding
	// itself; a server that gzips unconditionally still needs handling here.
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, gzErr := gzip.NewReader(reader)
		if gzErr != nil {
			return out, fmt.Errorf("failed to decompress gzip response: %w", gzErr)
		}
		defer gz.Close()
		reader = io.LimitReader(gz, maxBodyBytes)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return out, fmt.Errorf("failed to read response body: %w", err)
	}
	out.Body = body

	if resp.StatusCode != http.StatusOK {
		return out, statusError(resp.StatusCode, body)
	}
	return out, nil
}

// statusError builds a descriptive error for a non-200 response, including a
// short body excerpt to make misrouted endpoints obvious in the logs.
func statusError(code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("access denied (status %d) - endpoint may require auth or use bot protection", code)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited (status 429)")
	case http.StatusNotFound:
		return fmt.Errorf("endpoint not found (status 404) - endpoint may be incorrect")
	}
	excerpt := strings.TrimSpace(string(body))
	if len(excerpt) > 120 {
		excerpt = excerpt[:120] + "..."
	}
	return fmt.Errorf("unexpected status %d: %s", code, excerpt)
}

// looksLikeHTML reports whether a response is an HTML document rather than the
// JSON/XML API we asked for - usually a login wall, a bot challenge, or a
// marketing page served in place of a missing endpoint.
func looksLikeHTML(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(string(body)))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}
