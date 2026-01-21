package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type ctxKeyStart struct{}

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func (r *Router) reverseProxy(nodeID string, target *url.URL) *httputil.ReverseProxy {
	key := target.String()

	r.rpMu.Lock()
	if p, ok := r.rpCache[key]; ok {
		r.rpMu.Unlock()
		return p
	}
	r.rpMu.Unlock()

	p := httputil.NewSingleHostReverseProxy(target)
	p.Transport = r.transport

	// Flush frequently to support chunked streaming (SSE-like).
	p.FlushInterval = 100 * time.Millisecond

	origDirector := p.Director
	p.Director = func(req *http.Request) {
		// Attach start timestamp to request context.
		ctx := context.WithValue(req.Context(), ctxKeyStart{}, time.Now())
		*req = *req.WithContext(ctx)

		origDirector(req)

		// Make sure Host is target host (some clients depend on it).
		req.Host = target.Host

		// Remove hop-by-hop request headers.
		for _, h := range hopByHopHeaders {
			req.Header.Del(h)
		}
		// Connection header can list additional hop-by-hop headers.
		if c := req.Header.Get("Connection"); c != "" {
			for _, f := range strings.Split(c, ",") {
				req.Header.Del(strings.TrimSpace(f))
			}
		}
	}

	p.ModifyResponse = func(resp *http.Response) error {
		// Record RTT (best-effort).
		if r.Latency != nil && resp != nil && resp.Request != nil {
			if v := resp.Request.Context().Value(ctxKeyStart{}); v != nil {
				if start, ok := v.(time.Time); ok && !start.IsZero() {
					r.Latency.ObserveOK(nodeID, time.Since(start))
				}
			}
		}

		// Remove hop-by-hop response headers.
		for _, h := range hopByHopHeaders {
			resp.Header.Del(h)
		}
		if c := resp.Header.Get("Connection"); c != "" {
			for _, f := range strings.Split(c, ",") {
				resp.Header.Del(strings.TrimSpace(f))
			}
		}
		return nil
	}

	p.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		// Record RTT as error (best-effort).
		if r.Latency != nil && req != nil {
			if v := req.Context().Value(ctxKeyStart{}); v != nil {
				if start, ok := v.(time.Time); ok && !start.IsZero() {
					r.Latency.ObserveError(nodeID, time.Since(start))
				}
			}
		}
		http.Error(w, "upstream error", http.StatusBadGateway)
	}

	r.rpMu.Lock()
	r.rpCache[key] = p
	r.rpMu.Unlock()

	return p
}
