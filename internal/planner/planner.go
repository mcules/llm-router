package planner

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/mcules/llm-router/internal/activity"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/state"
)

type UnloadSender interface {
	SendUnload(nodeID, requestID, modelID string) error
}

type Planner struct {
	Cluster  *state.ClusterState
	Policies *policy.Store
	Commands UnloadSender

	// MinFreeBytes triggers RAM pressure if node available RAM drops below this threshold.
	MinFreeBytes uint64

	// Tick frequency.
	Interval time.Duration
	Activity *activity.Log
}

func (p *Planner) Run(ctx context.Context) {
	t := time.NewTicker(p.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Planner) tick(ctx context.Context) {
	nodes := p.Cluster.Snapshot()
	now := time.Now()

	// 1) TTL unload pass (cheap and deterministic).
	for _, n := range nodes {
		if n.InflightRequests > 0 {
			continue
		}
		for _, m := range n.Models {
			if m.State != state.ModelReady {
				continue
			}

			pol, ok, err := p.Policies.GetPolicy(ctx, m.ModelID)
			if err != nil {
				log.Printf("planner: get policy: %v", err)
				continue
			}
			if !ok || pol.TTLSecs <= 0 || pol.Pinned {
				continue
			}

			loadedAt := m.LoadedSince
			if loadedAt.IsZero() {
				// Fallback: treat as recently seen.
				continue
			}

			if now.Sub(loadedAt) >= time.Duration(pol.TTLSecs)*time.Second {
				p.tryUnload(n.NodeID, m.ModelID, "ttl")
			}
		}
	}

	// 2) RAM pressure pass.
	for _, n := range nodes {
		if p.MinFreeBytes == 0 {
			continue
		}
		if n.RAMAvailBytes >= p.MinFreeBytes {
			continue
		}
		if n.InflightRequests > 0 {
			// Conservative: avoid unloading while node is busy.
			continue
		}

		need := p.MinFreeBytes - n.RAMAvailBytes
		p.handlePressure(ctx, n, need)
	}
}

func (p *Planner) handlePressure(ctx context.Context, n *state.NodeSnapshot, needBytes uint64) {
	type cand struct {
		modelID     string
		score       int
		loadedSince time.Time
		ramBytes    uint64
	}
	var cands []cand

	// Build candidates: READY + not pinned.
	for _, m := range n.Models {
		if m.State != state.ModelReady {
			continue
		}
		pol, ok, err := p.Policies.GetPolicy(ctx, m.ModelID)
		if err != nil {
			log.Printf("planner: get policy: %v", err)
			continue
		}
		if ok && pol.Pinned {
			continue
		}

		var prio int
		var ram uint64
		if ok {
			prio = pol.Priority
			ram = pol.RAMRequiredBytes
		}

		// Lower score = unload earlier.
		// We unload low-priority models first, then older ones.
		score := prio

		cands = append(cands, cand{
			modelID:     m.ModelID,
			score:       score,
			loadedSince: m.LoadedSince,
			ramBytes:    ram,
		})
	}

	// Sort: lowest priority first, then oldest first.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		// Oldest first
		ti := cands[i].loadedSince
		tj := cands[j].loadedSince
		if ti.IsZero() && tj.IsZero() {
			return cands[i].modelID < cands[j].modelID
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		return ti.Before(tj)
	})

	var freed uint64
	for _, c := range cands {
		p.tryUnload(n.NodeID, c.modelID, "pressure")
		// Best-effort freed estimation. If RAMRequiredBytes is unknown, treat as 0.
		freed += c.ramBytes
		if freed >= needBytes {
			break
		}
	}
}

func (p *Planner) tryUnload(nodeID, modelID, reason string) {
	reqID := fmt.Sprintf("unload-%s-%d", reason, time.Now().UnixNano())
	if err := p.Commands.SendUnload(nodeID, reqID, modelID); err != nil {
		log.Printf("planner: unload failed node=%s model=%s reason=%s err=%v", nodeID, modelID, reason, err)
		return
	}
	if p.Activity != nil {
		p.Activity.Add(activity.Event{
			At:     time.Now(),
			Type:   activity.EventType("ttl_unload"), // or pressure_unload based on reason
			NodeID: nodeID,
			Model:  modelID,
			Note:   reason,
		})
	}
	log.Printf("planner: unload requested node=%s model=%s reason=%s", nodeID, modelID, reason)

	// Log activity event (optional).
	if p.Activity != nil {
		var et activity.EventType
		switch reason {
		case "ttl":
			et = activity.EventTTLUnload
		case "pressure":
			et = activity.EventPressureUnload
		default:
			et = activity.EventType(reason)
		}
		p.Activity.Add(activity.Event{
			At:     time.Now(),
			Type:   et,
			NodeID: nodeID,
			Model:  modelID,
			Note:   reason,
		})
	}
}
