package perf

import (
	"sync"
	"time"
)

type NodeStats struct {
	// EWMA of TTFB in milliseconds.
	TTFBMsEWMA float64

	// Simple error counters (rolling counters, not time-windowed).
	Requests uint64
	Errors   uint64

	// Last update timestamps (for debugging/UI).
	LastTTFB  time.Time
	LastError time.Time
}

type Store struct {
	mu    sync.RWMutex
	alpha float64
	nodes map[string]*NodeStats
}

// New creates a store with EWMA alpha (0..1). Typical: 0.2.
func New(alpha float64) *Store {
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.2
	}
	return &Store{
		alpha: alpha,
		nodes: map[string]*NodeStats{},
	}
}

func (s *Store) ObserveTTFB(nodeID string, d time.Duration) {
	ms := float64(d.Milliseconds())

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.getOrCreateLocked(nodeID)
	st.Requests++
	st.LastTTFB = time.Now()

	if st.TTFBMsEWMA == 0 {
		st.TTFBMsEWMA = ms
		return
	}
	st.TTFBMsEWMA = s.alpha*ms + (1.0-s.alpha)*st.TTFBMsEWMA
}

func (s *Store) ObserveError(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.getOrCreateLocked(nodeID)
	st.Requests++
	st.Errors++
	st.LastError = time.Now()
}

func (s *Store) Snapshot(nodeID string) (NodeStats, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.nodes[nodeID]
	if !ok {
		return NodeStats{}, false
	}
	return *st, true
}

func (s *Store) getOrCreateLocked(nodeID string) *NodeStats {
	if st, ok := s.nodes[nodeID]; ok {
		return st
	}
	st := &NodeStats{}
	s.nodes[nodeID] = st
	return st
}

func ErrorRate(st NodeStats) float64 {
	if st.Requests == 0 {
		return 0
	}
	return float64(st.Errors) / float64(st.Requests)
}
