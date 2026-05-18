package gate

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
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
func Middleware(checker *Checker, checkTimeout time.Duration) func(http.Handler) http.Handler {
	if checkTimeout <= 0 {
		checkTimeout = 500 * time.Millisecond
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := ExtractAPIKey(r)
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			defer cancel()

			allowed, errResp := checker.Check(ctx, apiKey)
			if allowed {
				next.ServeHTTP(w, r)
				return
			}
			WriteErrorResponse(w, errResp)
		})
	}
}
