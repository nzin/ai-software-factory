package coordinator

import (
	"encoding/json"
	"sync"
)

// Store persists runs. The default is in-memory; internal/coordinator/runstore
// provides a GORM/SQLite implementation that survives a coordinator restart.
type Store interface {
	Put(r *Run) error
	Get(id string) (*Run, bool, error)
	All() ([]*Run, error)
	Delete(id string) error
}

// memStore is the default in-memory Store.
type memStore struct {
	mu   sync.RWMutex
	runs map[string]*Run
}

// NewMemStore returns an empty in-memory Store.
func NewMemStore() Store {
	return &memStore{runs: make(map[string]*Run)}
}

// snapshot deep-copies a Run so callers never share the pointer the drive
// goroutine is mutating. This mirrors the GORM store, which serializes every
// Run, and keeps concurrent Get/Put race-free.
func snapshot(r *Run) *Run {
	b, err := json.Marshal(r)
	if err != nil {
		return r
	}
	var out Run
	if err := json.Unmarshal(b, &out); err != nil {
		return r
	}
	return &out
}

func (s *memStore) Put(r *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[r.ID] = snapshot(r)
	return nil
}

func (s *memStore) Get(id string) (*Run, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, false, nil
	}
	return snapshot(r), true, nil
}

func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
	return nil
}

func (s *memStore) All() ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, snapshot(r))
	}
	return out, nil
}
