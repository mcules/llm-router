package proxy

import (
	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/state"
)

// inflightPenaltyBytes is the per-inflight penalty applied to the score.
// We treat inflight as a proxy for latency / queueing.
const inflightPenaltyBytes = 512 * 1024 * 1024 // 512 MiB

// latencyPenaltyBytesPerMs converts EWMA latency into a score penalty.
// Tuning: 8 MiB/ms => 100ms ~ 800MiB penalty (strong preference for low-latency nodes).
const latencyPenaltyBytesPerMs = 8 * 1024 * 1024

// scoreNode returns a comparable score where higher is better.
func scoreNode(n *state.NodeSnapshot, lat *metrics.LatencyTracker) int64 {
	ram := int64(n.RAMAvailBytes)
	pen := int64(n.InflightRequests) * int64(inflightPenaltyBytes)

	var latPen int64
	if lat != nil {
		if l, ok := lat.Get(n.NodeID); ok && l.EWMAms > 0 {
			latPen = int64(l.EWMAms) * int64(latencyPenaltyBytesPerMs)
		}
	}

	return ram - pen - latPen
}

func pickBestByScore(nodes []*state.NodeSnapshot, lat *metrics.LatencyTracker) *state.NodeSnapshot {
	var best *state.NodeSnapshot
	var bestScore int64

	for _, n := range nodes {
		if best == nil {
			best = n
			bestScore = scoreNode(n, lat)
			continue
		}
		s := scoreNode(n, lat)
		if s > bestScore {
			best = n
			bestScore = s
		}
	}
	return best
}
