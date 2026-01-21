package state

import (
	"sync"
	"time"
)

// Comments in this file are intentionally in English.

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
	LastHeartbeat    time.Time
	RAMTotalBytes    uint64
	RAMAvailBytes    uint64
	InflightRequests uint32
	Models           map[string]ModelResidency
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

func (cs *ClusterState) UpsertNodeHello(nodeID, version, llamaBaseURL string) {
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
	n.LastHeartbeat = time.Now()
}

func (cs *ClusterState) UpdateNodeStatus(nodeID string, ramTotal, ramAvail uint64, inflight uint32, models map[string]ModelResidency) {
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
	n.RAMTotalBytes = ramTotal
	n.RAMAvailBytes = ramAvail
	n.InflightRequests = inflight
	n.LastHeartbeat = time.Now()
	n.Models = models
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

func cloneNode(n *NodeSnapshot) *NodeSnapshot {
	cp := *n
	cp.Models = make(map[string]ModelResidency, len(n.Models))
	for k, v := range n.Models {
		cp.Models[k] = v
	}
	return &cp
}
