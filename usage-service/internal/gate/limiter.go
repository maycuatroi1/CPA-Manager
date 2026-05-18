// Package gate implements the limit-gate sidecar: a small reverse proxy
// that sits in front of an upstream proxy (e.g. cliproxy) and consults
// the usage-service /check endpoint before forwarding each request.
package gate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrorResponse mirrors usage-service's ready-to-forward 429 payload.
type ErrorResponse struct {
	Status  int                    `json:"status"`
	Headers map[string]string      `json:"headers"`
	Body    map[string]interface{} `json:"body"`
}

// CheckResponse mirrors the subset of /check we consume here.
type CheckResponse struct {
	Allowed       bool           `json:"allowed"`
	HasLimit      bool           `json:"hasLimit"`
	LimitReached  bool           `json:"limitReached"`
	SoftLimitOnly bool           `json:"softLimitOnly"`
	ErrorResponse *ErrorResponse `json:"errorResponse,omitempty"`
}

// Checker calls usage-service /check. It is safe for concurrent use.
type Checker struct {
	UsageServiceURL string
	ManagementKey   string
	httpClient      *http.Client
}

// NewChecker constructs a Checker. timeout bounds the call to the
// usage-service so a slow usage-service does not slow request flow.
func NewChecker(usageServiceURL, managementKey string, timeout time.Duration) *Checker {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	return &Checker{
		UsageServiceURL: strings.TrimRight(usageServiceURL, "/"),
		ManagementKey:   managementKey,
		httpClient:      &http.Client{Timeout: timeout},
	}
}

// HashAPIKey mirrors usage-service's hashing (sha256 of trimmed key,
// lowercase hex). Exported so callers can pre-compute hashes if needed.
func HashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])
}

// Check looks up the limit state for apiKey. On any transport-level
// error it returns allowed=true (fail-open): a usage-service outage
// should not break the whole proxy.
func (c *Checker) Check(ctx context.Context, apiKey string) (bool, *ErrorResponse) {
	if apiKey == "" || c.UsageServiceURL == "" {
		return true, nil
	}
	hash := HashAPIKey(apiKey)
	endpoint := c.UsageServiceURL + "/v0/management/api-key-limits/" + url.PathEscape(hash) + "/check"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return true, nil
	}
	if c.ManagementKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.ManagementKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return true, nil
	}
	var out CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return true, nil
	}
	return out.Allowed, out.ErrorResponse
}

// WriteErrorResponse serializes errResp as the HTTP response. Falls back
// to a generic 429 body when errResp is nil.
func WriteErrorResponse(w http.ResponseWriter, errResp *ErrorResponse) {
	if errResp == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`))
		return
	}
	for k, v := range errResp.Headers {
		w.Header().Set(k, v)
	}
	if _, ok := errResp.Headers["Content-Type"]; !ok {
		w.Header().Set("Content-Type", "application/json")
	}
	status := errResp.Status
	if status == 0 {
		status = http.StatusTooManyRequests
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp.Body)
}

// ExtractAPIKey looks up the API key from the standard request locations:
// x-api-key header (Anthropic style), Authorization: Bearer (OpenAI/Codex
// style), and the api-key query parameter (legacy). Empty string when
// none are present.
func ExtractAPIKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		return k
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if k := strings.TrimSpace(r.URL.Query().Get("api-key")); k != "" {
		return k
	}
	return ""
}
