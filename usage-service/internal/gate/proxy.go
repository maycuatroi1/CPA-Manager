package gate

import (
	"context"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// NewReverseProxy returns an httputil.ReverseProxy targeting upstream.
// Host header is rewritten so virtual-hosted upstreams (e.g. cliproxy
// behind a TLS terminator) resolve to the right backend.
func NewReverseProxy(upstream *url.URL) *httputil.ReverseProxy {
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

// Middleware enforces per-key limits in front of upstream. checkTimeout
// bounds the call to usage-service; on timeout or any error the request
// is allowed through (fail-open).
//
// Each request is logged with a short prefix of the hash so operators
// can trace which key was checked and what decision was made. The full
// API key is never logged — only the first 8 hex chars of its sha256.
func Middleware(checker *Checker, checkTimeout time.Duration) func(http.Handler) http.Handler {
	if checkTimeout <= 0 {
		checkTimeout = 500 * time.Millisecond
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := ExtractAPIKey(r)
			if apiKey == "" {
				log.Printf("gate: %s %s no-api-key forward", r.Method, r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}
			hashPrefix := shortHash(apiKey)
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			defer cancel()

			allowed, errResp := checker.Check(ctx, apiKey)
			if allowed {
				log.Printf("gate: %s %s hash=%s allowed", r.Method, r.URL.Path, hashPrefix)
				next.ServeHTTP(w, r)
				return
			}
			log.Printf("gate: %s %s hash=%s BLOCKED 429", r.Method, r.URL.Path, hashPrefix)
			WriteErrorResponse(w, errResp)
		})
	}
}

func shortHash(apiKey string) string {
	h := HashAPIKey(apiKey)
	if len(h) < 8 {
		return h
	}
	// Format like "abcd1234…" so logs are scannable without leaking the
	// full hash (which itself is recoverable into the key space).
	return strings.ToLower(h[:8]) + "…"
}
