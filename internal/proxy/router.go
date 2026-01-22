package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/state"
)

type pickMode int

const (
	pickDirect pickMode = iota
	pickWait
)

type pickedNode struct {
	NodeID       string
	DataPlaneURL string
}

type modelGate struct {
	mu          sync.Mutex
	loadingNode string
	notifyCh    chan struct{} // closed when model becomes READY somewhere
}

func newModelGate() *modelGate {
	return &modelGate{
		notifyCh: make(chan struct{}),
	}
}

type Router struct {
	Cluster *state.ClusterState

	// Nodes with heartbeat older than this TTL are considered offline.
	NodeOfflineTTL time.Duration

	// Optional RTT tracker (server-side).
	Latency *metrics.LatencyTracker

	transport *http.Transport

	rpMu    sync.Mutex
	rpCache map[string]*httputil.ReverseProxy

	gatesMu sync.Mutex
	gates   map[string]*modelGate

	Policies *policy.Store
}

func NewRouter(cluster *state.ClusterState, policies *policy.Store) *Router {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Router{
		Cluster:        cluster,
		Policies:       policies,
		NodeOfflineTTL: 5 * time.Second,
		Latency:        nil,
		transport:      tr,
		rpCache:        map[string]*httputil.ReverseProxy{},
		gates:          map[string]*modelGate{},
	}
}

func (r *Router) getGate(modelID string) *modelGate {
	r.gatesMu.Lock()
	defer r.gatesMu.Unlock()

	g := r.gates[modelID]
	if g == nil {
		g = newModelGate()
		r.gates[modelID] = g
	}
	return g
}

// NotifyModelReady can be called by the control plane when a node reports READY for a model.
func (r *Router) NotifyModelReady(nodeID, modelID string) {
	g := r.getGate(modelID)

	g.mu.Lock()
	defer g.mu.Unlock()

	g.loadingNode = ""

	// Wake waiters.
	close(g.notifyCh)
	g.notifyCh = make(chan struct{})
}

// NotifyModelState implements control.ModelStateNotifier.
// It is intentionally minimal: we only need to wake waiters when a model becomes READY.
// Other states are ignored for placement purposes.
func (r *Router) NotifyModelState(nodeID, modelID string, st state.ModelState) {
	if st == state.ModelReady {
		r.NotifyModelReady(nodeID, modelID)
	}
}

// waitModelReady waits until the selected node reports the model as READY (or we get a READY notify).
func (r *Router) waitModelReady(modelID, nodeID string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	g := r.getGate(modelID)

	// Fast path: already READY on this node.
	if r.isModelReadyOnNode(modelID, nodeID) {
		return nil
	}

	for {
		g.mu.Lock()
		ch := g.notifyCh
		g.mu.Unlock()

		select {
		case <-deadline.C:
			return errors.New("timeout waiting for model readiness")
		case <-ch:
			if r.isModelReadyOnNode(modelID, nodeID) {
				return nil
			}
		case <-time.After(200 * time.Millisecond):
			if r.isModelReadyOnNode(modelID, nodeID) {
				return nil
			}
		}
	}
}

func (r *Router) isModelReadyOnNode(modelID, nodeID string) bool {
	snap := r.Cluster.Snapshot()
	for _, n := range snap {
		if n.NodeID != nodeID {
			continue
		}
		if m, ok := n.Models[modelID]; ok && m.State == state.ModelReady {
			return true
		}
	}
	return false
}

// extractModelAndBody parses the request JSON body and extracts the "model" field.
// It returns the model id and the raw body bytes for re-use in the proxy.
func extractModelAndBody(req *http.Request) (string, []byte, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read body: %w", err)
	}
	_ = req.Body.Close()

	var tmp struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &tmp); err != nil {
		return "", nil, fmt.Errorf("invalid json: %w", err)
	}
	if tmp.Model == "" {
		return "", nil, errors.New("missing model field")
	}

	// Restore body for potential downstream reads (caller typically re-sets it anyway).
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))

	return tmp.Model, raw, nil
}

func (r *Router) buildTarget(node pickedNode) (*url.URL, error) {
	u, err := url.Parse(node.DataPlaneURL)
	if err != nil {
		return nil, err
	}
	return u, nil
}
