package metrics

import (
	"sync"
	"time"
)

type NodeLatency struct {
	// EWMA of RTT in milliseconds.
	EWMAms float64

	// Counters (rolling since start).
	OK    uint64
	Error uint64

	// Last observed RTT.
	LastRTT time.Duration

	// Timestamp of last observation.
	LastAt time.Time
}

type LatencyTracker struct {
	mu    sync.RWMutex
	alpha float64
	nodes map[string]*NodeLatency
}

// NewLatencyTracker creates a tracker with EWMA smoothing factor alpha.
// Typical alpha: 0.1..0.3 (higher reacts faster).
func NewLatencyTracker(alpha float64) *LatencyTracker {
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.2
	}
	return &LatencyTracker{
		alpha: alpha,
		nodes: map[string]*NodeLatency{},
	}
}

func (t *LatencyTracker) ObserveOK(nodeID string, rtt time.Duration) {
	t.observe(nodeID, rtt, true)
}

func (t *LatencyTracker) ObserveError(nodeID string, rtt time.Duration) {
	t.observe(nodeID, rtt, false)
}

func (t *LatencyTracker) observe(nodeID string, rtt time.Duration, ok bool) {
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	n := t.nodes[nodeID]
	if n == nil {
		n = &NodeLatency{}
		t.nodes[nodeID] = n
	}

	ms := float64(rtt.Milliseconds())
	if ms < 0 {
		ms = 0
	}

	if n.EWMAms == 0 {
		n.EWMAms = ms
	} else {
		n.EWMAms = (t.alpha * ms) + ((1.0 - t.alpha) * n.EWMAms)
	}

	n.LastRTT = rtt
	n.LastAt = now
	if ok {
		n.OK++
	} else {
		n.Error++
	}
}

func (t *LatencyTracker) Get(nodeID string) (NodeLatency, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	n := t.nodes[nodeID]
	if n == nil {
		return NodeLatency{}, false
	}
	return *n, true
}

func (t *LatencyTracker) Snapshot() map[string]NodeLatency {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make(map[string]NodeLatency, len(t.nodes))
	for k, v := range t.nodes {
		out[k] = *v
	}
	return out
}

func (t *LatencyTracker) Delete(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.nodes, nodeID)
}
