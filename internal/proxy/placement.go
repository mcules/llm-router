package proxy

import (
	"errors"
	"time"

	"github.com/mcules/llm-router/internal/state"
)

// PlacementResult describes the outcome of a placement decision.
type PlacementResult struct {
	NodeID       string
	DataPlaneURL string
	Mode         pickMode
}

// pickNodeForModel is the high-level placement entry point.
// It is intentionally kept small and deterministic.
func (r *Router) pickNodeForModel(modelID string) (pickedNode, pickMode, error) {
	now := time.Now()

	// Only consider online nodes.
	snap := r.Cluster.SnapshotOnline(now, r.NodeOfflineTTL)

	// 1) If any node reports READY for this model, route directly.
	for _, n := range snap {
		if n.DataPlaneURL == "" {
			continue
		}
		if m, ok := n.Models[modelID]; ok && m.State == state.ModelReady {
			return pickedNode{NodeID: n.NodeID, DataPlaneURL: n.DataPlaneURL}, pickDirect, nil
		}
	}

	// 2) Gate-based loader coordination.
	g := r.getGate(modelID)
	g.mu.Lock()
	defer g.mu.Unlock()

	// If we have a known ready node, try it.
	if g.readyNode != "" {
		for _, n := range snap {
			if n.NodeID == g.readyNode && n.DataPlaneURL != "" {
				return pickedNode{NodeID: n.NodeID, DataPlaneURL: n.DataPlaneURL}, pickDirect, nil
			}
		}
		// Ready node went away.
		g.readyNode = ""
	}

	// If a loader is in progress, callers can wait.
	if g.loadingNode != "" {
		for _, n := range snap {
			if n.NodeID == g.loadingNode && n.DataPlaneURL != "" {
				return pickedNode{NodeID: n.NodeID, DataPlaneURL: n.DataPlaneURL}, pickWait, nil
			}
		}
		// Loader node went away.
		g.loadingNode = ""
	}

	// 3) Choose best online eligible node by score (RAM - inflight - latency penalty).
	eligible := make([]*state.NodeSnapshot, 0, len(snap))
	for _, n := range snap {
		if n.DataPlaneURL != "" {
			eligible = append(eligible, n)
		}
	}
	best := pickBestByScore(eligible, r.Latency)
	if best == nil {
		return pickedNode{}, pickDirect, errors.New("no nodes available")
	}

	// Mark this node as the loading owner.
	g.loadingNode = best.NodeID

	return pickedNode{NodeID: best.NodeID, DataPlaneURL: best.DataPlaneURL}, pickDirect, nil
}
