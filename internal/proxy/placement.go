package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mcules/llm-router/internal/auth"
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
func (r *Router) pickNodeForModel(req *http.Request, modelID string) (pickedNode, pickMode, error) {
	now := time.Now()

	// 0) ACL Check
	authRecord := auth.GetAuthRecord(req)
	if authRecord != nil {
		if !auth.CheckACL(authRecord.AllowedModels, modelID) {
			return pickedNode{}, pickDirect, errors.New("access to model denied by ACL")
		}
	}

	// Only consider online nodes.
	snap := r.Cluster.SnapshotOnline(now, r.NodeOfflineTTL)

	// Filter nodes by ACL
	if authRecord != nil {
		filtered := make([]*state.NodeSnapshot, 0, len(snap))
		for _, n := range snap {
			if auth.CheckACL(authRecord.AllowedNodes, n.NodeID) {
				filtered = append(filtered, n)
			}
		}
		snap = filtered
	}

	// 1) If any node reports READY for this model, route to the best one among them.
	var readyNodes []*state.NodeSnapshot
	for _, n := range snap {
		if n.DataPlaneURL == "" {
			continue
		}
		if m, ok := n.Models[modelID]; ok && m.State == state.ModelReady {
			readyNodes = append(readyNodes, n)
		}
	}

	if len(readyNodes) > 0 {
		pol, _, _ := r.Policies.GetPolicy(context.Background(), modelID)
		best := pickBestByScore(readyNodes, r.Latency, pol)
		if best != nil {
			return pickedNode{NodeID: best.NodeID, DataPlaneURL: best.DataPlaneURL}, pickDirect, nil
		}
	}

	// 2) Gate-based loader coordination.
	g := r.getGate(modelID)
	g.mu.Lock()
	defer g.mu.Unlock()

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

	pol, _, _ := r.Policies.GetPolicy(context.Background(), modelID)

	best := pickBestByScore(eligible, r.Latency, pol)
	if best == nil {
		return pickedNode{}, pickDirect, errors.New("no nodes available")
	}

	// Mark this node as the loading owner.
	g.loadingNode = best.NodeID

	return pickedNode{NodeID: best.NodeID, DataPlaneURL: best.DataPlaneURL}, pickDirect, nil
}
