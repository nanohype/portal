package logstream

import "sync"

// Streamer manages log streaming for runs.
type Streamer interface {
	Publish(runID string, data []byte)
	Subscribe(runID string) <-chan []byte
	Unsubscribe(runID string, ch <-chan []byte)
	Close(runID string)
}

// runState holds the subscriber list and log buffer for a single run.
type runState struct {
	subs   []chan []byte
	buffer [][]byte
	closed bool
}

// MemoryStreamer is an in-memory fan-out streamer for development.
// It buffers all published messages so late-joining subscribers get a replay.
type MemoryStreamer struct {
	mu   sync.Mutex
	runs map[string]*runState
}

func NewMemoryStreamer() *MemoryStreamer {
	return &MemoryStreamer{
		runs: make(map[string]*runState),
	}
}

func (s *MemoryStreamer) getOrCreate(runID string) *runState {
	rs, ok := s.runs[runID]
	if !ok {
		rs = &runState{}
		s.runs[runID] = rs
	}
	return rs
}

func (s *MemoryStreamer) Publish(runID string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rs := s.getOrCreate(runID)
	if rs.closed {
		return
	}

	// Buffer a copy so it survives after the caller reuses the slice
	cp := make([]byte, len(data))
	copy(cp, data)
	rs.buffer = append(rs.buffer, cp)

	for _, ch := range rs.subs {
		select {
		case ch <- cp:
		default:
			// Drop message if subscriber is slow
		}
	}
}

func (s *MemoryStreamer) Subscribe(runID string) <-chan []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	rs := s.getOrCreate(runID)
	ch := make(chan []byte, 256)

	// Replay buffered log lines so late joiners see prior output
	for _, msg := range rs.buffer {
		select {
		case ch <- msg:
		default:
		}
	}

	// If the run already finished, close immediately after replay
	if rs.closed {
		close(ch)
		return ch
	}

	rs.subs = append(rs.subs, ch)
	return ch
}

func (s *MemoryStreamer) Unsubscribe(runID string, ch <-chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rs := s.runs[runID]
	if rs == nil {
		return
	}

	for i, sub := range rs.subs {
		if sub == ch {
			rs.subs = append(rs.subs[:i], rs.subs[i+1:]...)
			close(sub)
			break
		}
	}
}

func (s *MemoryStreamer) Close(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rs := s.runs[runID]
	if rs == nil {
		return
	}

	for _, ch := range rs.subs {
		close(ch)
	}
	rs.subs = nil
	rs.closed = true

	// Keep buffer around for late subscribers; clean up after a while
	// by capping the number of finished runs we retain.
	const maxFinished = 100
	if s.countClosed() > maxFinished {
		s.evictOldest()
	}
}

func (s *MemoryStreamer) countClosed() int {
	n := 0
	for _, rs := range s.runs {
		if rs.closed {
			n++
		}
	}
	return n
}

func (s *MemoryStreamer) evictOldest() {
	// Simple eviction: delete the first closed run we find
	for id, rs := range s.runs {
		if rs.closed {
			delete(s.runs, id)
			return
		}
	}
}
