package activity

import (
	"sync"
	"time"
)

type EventType string

const (
	EventPressureUnload EventType = "pressure_unload"
	EventTTLUnload      EventType = "ttl_unload"
	EventManualUnload   EventType = "manual_unload"
)

type Event struct {
	At     time.Time
	Type   EventType
	NodeID string
	Model  string
	Note   string
}

type Log struct {
	mu   sync.RWMutex
	buf  []Event
	next int
	full bool
}

func New(size int) *Log {
	if size <= 0 {
		size = 200
	}
	return &Log{
		buf: make([]Event, size),
	}
}

func (l *Log) Add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf[l.next] = e
	l.next++
	if l.next >= len(l.buf) {
		l.next = 0
		l.full = true
	}
}

func (l *Log) List() []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if !l.full && l.next == 0 {
		return nil
	}

	var out []Event
	if l.full {
		out = make([]Event, 0, len(l.buf))
		out = append(out, l.buf[l.next:]...)
		out = append(out, l.buf[:l.next]...)
	} else {
		out = append([]Event(nil), l.buf[:l.next]...)
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
