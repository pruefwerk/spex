package resourceclaims

import (
	"context"
	"sync"
)

// MemoryStore coordinates only callers sharing this instance in one process.
// It cannot coordinate separate GitHub jobs and does not survive worker loss.
type MemoryStore struct {
	mu    sync.Mutex
	state State
}

func (s *MemoryStore) Transaction(ctx context.Context, transaction func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state := clone(s.state)
	if err := transaction(&state); err != nil {
		return err
	}
	if err := validate(state); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state = clone(state)
	return nil
}
