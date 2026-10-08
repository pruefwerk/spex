package scheduling

import (
	"context"
	"slices"
	"sync"
)

// MemoryStore is for unit tests, not coordination between independent runners.
type MemoryStore struct {
	mu    sync.Mutex
	state State
}

func (s *MemoryStore) Transaction(ctx context.Context, action func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := s.state
	copy.Entries = slices.Clone(copy.Entries)
	if err := action(&copy); err != nil {
		return err
	}
	if validate(copy) != nil {
		return ErrStore
	}
	s.state = copy
	return nil
}
