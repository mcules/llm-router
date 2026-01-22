package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HandleCompletions proxies POST /v1/completions (legacy OpenAI endpoint) to the selected node.
// It supports both non-stream and stream responses by passing through the response body as-is.
func (r *Router) HandleCompletions(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}

	modelID, body, err := extractModelAndBody(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	node, mode, err := r.pickNodeForModel(req, modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if mode == pickWait {
		if err := r.waitModelReady(modelID, node.NodeID, 180*time.Second); err != nil {
			http.Error(w, "model is still loading (timeout)", http.StatusServiceUnavailable)
			return
		}
	}

	target, err := url.Parse(node.DataPlaneURL)
	if err != nil {
		http.Error(w, "invalid node data plane url", http.StatusBadGateway)
		return
	}

	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	r.reverseProxy(node.NodeID, target).ServeHTTP(w, req)
}
