package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// isClientForwardPath returns true for the LLM-style API paths that
// cpa-manager should forward to the upstream proxy after running a
// limit check. /v0/management/* is handled separately by handleProxy,
// /v1/models is handled by handleModelListProxy; everything else under
// /v1/ and /v1beta/ goes through the limit-aware forwarder.
func isClientForwardPath(path string) bool {
	clean := strings.TrimRight(path, "/")
	if clean == "/v1" || clean == "/v1beta" {
		return true
	}
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/")
}

// extractClientAPIKey looks up the API key the upstream proxy will use
// for auth. Order matters: x-api-key (Anthropic style) wins so that
// requests including BOTH x-api-key and a Bearer OAuth token are
// reported by their primary credential.
func extractClientAPIKey(r *http.Request) string {
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

func hashClientAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])
}

func shortHash(hash string) string {
	if len(hash) < 8 {
		return hash
	}
	return hash[:8] + "…"
}

// handleClientForward enforces per-key limits and forwards the request
// to the configured upstream proxy. It is functionally equivalent to
// the legacy out-of-process limit-gate sidecar, but skips the HTTP
// round-trip to /check by calling the store directly.
func (s *Server) handleClientForward(w http.ResponseWriter, r *http.Request) {
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok || setup.CPAUpstreamURL == "" {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	target, err := url.Parse(setup.CPAUpstreamURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	apiKey := extractClientAPIKey(r)
	if apiKey != "" {
		hash := hashClientAPIKey(apiKey)

		// Priority gate: high-priority keys bypass any waiting that
		// low-priority callers may incur under contention.
		preItem, found, lookupErr := s.store.CheckAPIKeyLimit(r.Context(), hash)
		highPriority := found && lookupErr == nil && preItem.Priority
		release := s.priorityGate.enter(r.Context(), highPriority)
		defer release()

		// Re-query under the gate to refresh usage state.
		checkCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		item, found, err := s.store.CheckAPIKeyLimit(checkCtx, hash)
		cancel()
		switch {
		case err != nil:
			// Fail-open on store errors so an internal SQL hiccup
			// doesn't break upstream traffic.
			log.Printf("forward: %s %s hash=%s store-error=%v (fail-open)", r.Method, r.URL.Path, shortHash(hash), err)
		case found && item.LimitReached && !item.SoftLimit:
			log.Printf("forward: %s %s hash=%s BLOCKED 429 (tokens=%d cost=%.4f limit=%.2f type=%s)",
				r.Method, r.URL.Path, shortHash(hash),
				item.UsedTokens, item.UsedCost, item.LimitValue, item.LimitType)
			writeRateLimitResponse(w, buildClaudeRateLimitResponse(item))
			return
		default:
			log.Printf("forward: %s %s hash=%s allowed (hasLimit=%v reached=%v soft=%v)",
				r.Method, r.URL.Path, shortHash(hash),
				found, found && item.LimitReached, found && item.SoftLimit)
		}
	}

	proxy := newClientReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

// newClientReverseProxy targets upstream cliproxy. Unlike handleProxy
// (which injects the management Bearer token for /v0/management/*),
// this proxy passes through whatever auth the client already sent so
// cliproxy can authenticate the end user as usual.
func newClientReverseProxy(upstream *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = upstream.Scheme
		req.URL.Host = upstream.Host
		req.Host = upstream.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"upstream_error","message":"upstream proxy unreachable"}}`))
	}
	return proxy
}

// writeRateLimitResponse serializes a Claude-style 429 onto w.
func writeRateLimitResponse(w http.ResponseWriter, resp rateLimitErrorResponse) {
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusTooManyRequests
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp.Body)
}
