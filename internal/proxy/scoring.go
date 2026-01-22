package proxy

import (
	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/state"
)

// inflightPenaltyBytes is the per-inflight penalty applied to the score.
// We treat inflight as a proxy for latency / queueing.
const inflightPenaltyBytes = 512 * 1024 * 1024 // 512 MiB

// latencyPenaltyBytesPerMs converts EWMA latency into a score penalty.
// Tuning: 8 MiB/ms => 100ms ~ 800MiB penalty (strong preference for low-latency nodes).
const latencyPenaltyBytesPerMs = 8 * 1024 * 1024

// scoreNode returns a comparable score where higher is better.
func scoreNode(n *state.NodeSnapshot, lat *metrics.LatencyTracker, p policy.ModelPolicy) int64 {
	ram := int64(n.RAMAvailBytes)

	// OOM Protection: If we know the RAM requirements and it doesn't fit,
	// give it a massive penalty.
	if p.RAMRequiredBytes > 0 && n.RAMAvailBytes < p.RAMRequiredBytes {
		return -1e15 // Extremely low score
	}

	pen := int64(n.InflightRequests) * int64(inflightPenaltyBytes)

	var latPen int64
	if lat != nil {
		if l, ok := lat.Get(n.NodeID); ok && l.EWMAms > 0 {
			latPen = int64(l.EWMAms) * int64(latencyPenaltyBytesPerMs)
		}
	}

	// Warm affinity: if the model is already on this node (even if not READY yet),
	// give it a small bonus to prefer reusing the node.
	var affinityBonus int64
	if _, ok := n.Models[p.ModelID]; ok {
		affinityBonus = 1024 * 1024 * 1024 // 1 GiB bonus
	}

	return ram - pen - latPen + affinityBonus
}

func pickBestByScore(nodes []*state.NodeSnapshot, lat *metrics.LatencyTracker, p policy.ModelPolicy) *state.NodeSnapshot {
	var best *state.NodeSnapshot
	var bestScore int64

	for _, n := range nodes {
		s := scoreNode(n, lat, p)
		if best == nil || s > bestScore {
			best = n
			bestScore = s
		} else if s == bestScore && best != nil {
			// Tie-breaker: prefer node with fewer inflight requests
			if n.InflightRequests < best.InflightRequests {
				best = n
			} else if n.InflightRequests == best.InflightRequests {
				// Second tie-breaker: stable but fair based on NodeID
				if n.NodeID < best.NodeID {
					best = n
				}
			}
		}
	}
	return best
}
