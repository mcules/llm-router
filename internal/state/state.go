package state

import (
	"log"
	"sync"
	"time"
)

type ModelState string

const (
	ModelLoading  ModelState = "loading"
	ModelReady    ModelState = "ready"
	ModelUnloaded ModelState = "unloaded"
	ModelError    ModelState = "error"
)

type ModelResidency struct {
	ModelID     string
	State       ModelState
	LoadedSince time.Time
	LastSeen    time.Time
}

type NodeSnapshot struct {
	NodeID           string
	Version          string
	LlamaBaseURL     string
	DataPlaneURL     string
	LastHeartbeat    time.Time
	RAMTotalBytes    uint64
	RAMAvailBytes    uint64
	InflightRequests uint32
	Models           map[string]ModelResidency
}

// IsOnline returns true if the node heartbeat is within the given TTL.
func (n *NodeSnapshot) IsOnline(now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return true
	}
	if n.LastHeartbeat.IsZero() {
		return false
	}
	return now.Sub(n.LastHeartbeat) <= ttl
}

type ClusterState struct {
	mu    sync.RWMutex
	nodes map[string]*NodeSnapshot
}

func NewClusterState() *ClusterState {
	return &ClusterState{
		nodes: map[string]*NodeSnapshot{},
	}
}

func (cs *ClusterState) UpsertNodeHello(nodeID, version, llamaBaseURL, dataPlaneURL string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	n, ok := cs.nodes[nodeID]
	if !ok {
		n = &NodeSnapshot{
			NodeID: nodeID,
			Models: map[string]ModelResidency{},
		}
		cs.nodes[nodeID] = n
	}
	n.Version = version
	n.LlamaBaseURL = llamaBaseURL
	n.DataPlaneURL = dataPlaneURL
	n.LastHeartbeat = time.Now()
}

func (cs *ClusterState) UpdateNodeStatus(nodeID string, ramTotal, ramAvail uint64, inflight uint32, models map[string]ModelResidency) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	n, ok := cs.nodes[nodeID]
	if !ok {
		// This should ideally not happen if Hello was sent, but we handle it.
		n = &NodeSnapshot{
			NodeID: nodeID,
			Models: map[string]ModelResidency{},
		}
		cs.nodes[nodeID] = n
	}
	n.RAMTotalBytes = ramTotal
	n.RAMAvailBytes = ramAvail
	n.InflightRequests = inflight
	n.LastHeartbeat = time.Now()
	n.Models = models
	log.Printf("DEBUG: ClusterState updated node %s, last_heartbeat=%v, total nodes: %d", nodeID, n.LastHeartbeat.Format("15:04:05.000"), len(cs.nodes))
}

func (cs *ClusterState) Snapshot() []*NodeSnapshot {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	out := make([]*NodeSnapshot, 0, len(cs.nodes))
	for _, n := range cs.nodes {
		out = append(out, cloneNode(n))
	}
	return out
}

// SnapshotOnline returns a snapshot filtered by heartbeat TTL.
func (cs *ClusterState) SnapshotOnline(now time.Time, ttl time.Duration) []*NodeSnapshot {
	all := cs.Snapshot()
	if ttl <= 0 {
		return all
	}
	out := make([]*NodeSnapshot, 0, len(all))
	for _, n := range all {
		if n.IsOnline(now, ttl) {
			out = append(out, n)
		}
	}
	return out
}

func cloneNode(n *NodeSnapshot) *NodeSnapshot {
	cp := *n
	cp.Models = make(map[string]ModelResidency, len(n.Models))
	for k, v := range n.Models {
		cp.Models[k] = v
	}
	return &cp
}
