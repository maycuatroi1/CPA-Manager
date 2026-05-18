// limit-gate is a small reverse-proxy sidecar that enforces per-API-key
// usage limits in front of an upstream proxy (cliproxy). For each
// inbound request it extracts the API key, calls usage-service's
// /v0/management/api-key-limits/{hash}/check endpoint, and either
// forwards the request or returns the Claude-style 429 errorResponse
// embedded in the /check payload.
//
// Configuration is via environment variables:
//
//   LIMIT_GATE_LISTEN          listen address (default ":8788")
//   LIMIT_GATE_UPSTREAM        upstream URL, e.g. "https://cliproxy.omelet.tech"
//   LIMIT_GATE_USAGE_URL       usage-service base, e.g. "http://cpa-manager:18317"
//   LIMIT_GATE_MANAGEMENT_KEY  bearer token for usage-service (optional)
//   LIMIT_GATE_CHECK_TIMEOUT   per-request /check timeout (default "500ms")
package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/gate"
)

func main() {
	listen := envOrDefault("LIMIT_GATE_LISTEN", ":8788")
	upstreamRaw := strings.TrimSpace(os.Getenv("LIMIT_GATE_UPSTREAM"))
	usageURL := strings.TrimSpace(os.Getenv("LIMIT_GATE_USAGE_URL"))
	mgmtKey := strings.TrimSpace(os.Getenv("LIMIT_GATE_MANAGEMENT_KEY"))
	timeoutRaw := envOrDefault("LIMIT_GATE_CHECK_TIMEOUT", "500ms")

	if upstreamRaw == "" {
		log.Fatal(errors.New("LIMIT_GATE_UPSTREAM is required"))
	}
	upstream, err := url.Parse(upstreamRaw)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		log.Fatalf("invalid LIMIT_GATE_UPSTREAM %q: %v", upstreamRaw, err)
	}
	if usageURL == "" {
		log.Println("warning: LIMIT_GATE_USAGE_URL is empty — gate will pass all requests through unchecked")
	}
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil {
		log.Fatalf("invalid LIMIT_GATE_CHECK_TIMEOUT %q: %v", timeoutRaw, err)
	}

	checker := gate.NewChecker(usageURL, mgmtKey, timeout)
	proxy := gate.NewReverseProxy(upstream)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", gate.Middleware(checker, timeout)(proxy))

	server := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("limit-gate listening on %s -> %s (usage=%s, timeout=%s)", listen, upstream.String(), usageURL, timeout)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
