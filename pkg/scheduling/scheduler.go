// Package scheduling admits participating executions in FIFO order within a
// host-selected capacity pool. It does not lock application resources or decide
// which infrastructure belongs to an execution.
package scheduling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

var (
	ErrStore     = errors.New("scheduling store unavailable or incompatible")
	ErrBusy      = errors.New("execution waiting for capacity")
	ErrFull      = errors.New("scheduling queue is full")
	ErrDuplicate = errors.New("request already queued or running")
	ErrOwnership = errors.New("scheduling ownership unavailable")
)

type Entry struct {
	Owner    string `json:"owner"`
	Request  string `json:"request"`
	Worker   string `json:"worker"`
	Sequence uint64 `json:"sequence"`
	Status   string `json:"status"`
}

type State struct {
	Pool     string  `json:"pool"`
	Capacity int     `json:"capacity"`
	Next     uint64  `json:"next"`
	Entries  []Entry `json:"entries"`
}

// Store atomically commits a complete transaction across participating workers.
// Callback errors must leave state unchanged. Callbacks may retry: they must
// perform no I/O, have no external effects, and not retain the state pointer.
type Store interface {
	Transaction(context.Context, func(*State) error) error
}

type Scheduler struct {
	Store        Store
	Pool         string
	Capacity     int
	PollInterval time.Duration
}

type Report struct {
	Pool             string `json:"pool"`
	Capacity         int    `json:"capacity"`
	Owner            string `json:"owner"`
	Request          string `json:"request"`
	Worker           string `json:"worker"`
	Sequence         uint64 `json:"sequence"`
	Status           string `json:"status"`
	WaitMilliseconds int64  `json:"wait_ms"`
}

type Session struct {
	scheduler *Scheduler
	entry     Entry
}

func identifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validate(state State) error {
	if state.Pool == "" && state.Capacity == 0 && state.Next == 0 && len(state.Entries) == 0 {
		return nil
	}
	if !identifier(state.Pool, 256) || state.Capacity < 1 || state.Capacity > 64 || len(state.Entries) > 10000 {
		return ErrStore
	}
	owners, requests := map[string]bool{}, map[string]bool{}
	var previous uint64
	running := 0
	for _, entry := range state.Entries {
		owner, err := hex.DecodeString(entry.Owner)
		if err != nil || len(owner) != 16 || entry.Owner != hex.EncodeToString(owner) || owners[entry.Owner] || requests[entry.Request] || !identifier(entry.Request, 128) || !identifier(entry.Worker, 512) || entry.Sequence <= previous || entry.Sequence > state.Next {
			return ErrStore
		}
		owners[entry.Owner], requests[entry.Request] = true, true
		previous = entry.Sequence
		switch entry.Status {
		case "queued":
		case "running", "recovery_required":
			running++
		default:
			return ErrStore
		}
	}
	if running > state.Capacity {
		return ErrStore
	}
	return nil
}

func (s *Scheduler) transaction(ctx context.Context, action func(*State) error) error {
	if s == nil || s.Store == nil || !identifier(s.Pool, 256) || s.Capacity < 1 || s.Capacity > 64 || s.PollInterval < 0 {
		return ErrStore
	}
	return s.Store.Transaction(ctx, func(state *State) error {
		if validate(*state) != nil {
			return ErrStore
		}
		if state.Pool == "" {
			state.Pool, state.Capacity = s.Pool, s.Capacity
		}
		if state.Pool != s.Pool || state.Capacity != s.Capacity {
			return ErrStore
		}
		if err := action(state); err != nil {
			return err
		}
		return validate(*state)
	})
}

// Acquire enqueues before waiting. The durable sequence, not worker polling
// speed, defines admission order. A slot lasts through execution and cleanup.
// Neither queued requests nor running slots expire automatically.
func (s *Scheduler) Acquire(ctx context.Context, request, worker string) (*Session, Report, error) {
	report := Report{Request: request, Worker: worker, Status: "not_queued"}
	if s == nil || !identifier(request, 128) || !identifier(worker, 512) {
		return nil, report, ErrStore
	}
	report.Pool, report.Capacity = s.Pool, s.Capacity
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, report, ErrStore
	}
	entry := Entry{Owner: hex.EncodeToString(nonce[:]), Request: request, Worker: worker, Status: "queued"}
	report.Owner = entry.Owner
	start := time.Now()
	err := s.transaction(ctx, func(state *State) error {
		for _, existing := range state.Entries {
			if existing.Request == request {
				return ErrDuplicate
			}
		}
		if len(state.Entries) >= 10000 || state.Next == ^uint64(0) {
			return ErrFull
		}
		entry.Sequence = state.Next + 1
		state.Next = entry.Sequence
		state.Entries = append(state.Entries, entry)
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrDuplicate) && !errors.Is(err, ErrFull) {
			report.Status = "unknown"
		}
		return nil, report, err
	}
	report.Sequence, report.Status = entry.Sequence, "queued"
	interval := s.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	for {
		if ctx.Err() != nil {
			break
		}
		err = s.transaction(ctx, func(state *State) error {
			running, rank, index := 0, 0, -1
			for i, existing := range state.Entries {
				if existing.Status != "queued" {
					running++
				}
				if existing.Owner == entry.Owner {
					index = i
				}
				if existing.Status == "queued" && existing.Sequence < entry.Sequence {
					rank++
				}
			}
			if index < 0 || state.Entries[index] != entry {
				return ErrOwnership
			}
			if rank >= state.Capacity-running {
				return ErrBusy
			}
			state.Entries[index].Status = "running"
			return nil
		})
		report.WaitMilliseconds = time.Since(start).Milliseconds()
		if err == nil {
			entry.Status, report.Status = "running", "admitted"
			return &Session{s, entry}, report, nil
		}
		if !errors.Is(err, ErrBusy) {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				// Cancellation can race the transaction after the loop's check.
				// Reconcile with a fresh context: withdraw only a confirmed queued
				// entry, and retain capacity if admission might have committed.
				break
			}
			report.Status = "unknown"
			return nil, report, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	// Only a confirmed queued entry can be withdrawn without postconditions.
	// An uncertain write or running slot must remain reserved for reconciliation.
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = s.transaction(cleanup, func(state *State) error {
		for i, existing := range state.Entries {
			if existing.Owner == entry.Owner {
				if existing != entry || existing.Status != "queued" {
					return ErrOwnership
				}
				state.Entries = slices.Delete(state.Entries, i, i+1)
				return nil
			}
		}
		return ErrOwnership
	})
	report.WaitMilliseconds = time.Since(start).Milliseconds()
	report.Status = "cancelled"
	if err != nil {
		report.Status = "unknown"
	}
	return nil, report, ctx.Err()
}

// Finish releases capacity only after the host verifies stopped execution and
// completed capacity cleanup. Unverified slots remain counted against capacity.
func (s *Session) Finish(ctx context.Context, safe bool) error {
	if s == nil {
		return nil
	}
	return s.scheduler.transaction(ctx, func(state *State) error {
		for i, entry := range state.Entries {
			if entry.Owner != s.entry.Owner {
				continue
			}
			if entry != s.entry {
				return ErrOwnership
			}
			if safe {
				state.Entries = slices.Delete(state.Entries, i, i+1)
			} else {
				state.Entries[i].Status = "recovery_required"
			}
			return nil
		}
		return ErrOwnership
	})
}

func (s *Scheduler) Inspect(ctx context.Context) (State, error) {
	var snapshot State
	readOnly := errors.New("read-only scheduling inspection")
	err := s.transaction(ctx, func(state *State) error {
		snapshot = *state
		snapshot.Entries = slices.Clone(state.Entries)
		return readOnly
	})
	if !errors.Is(err, readOnly) {
		return State{}, ErrStore
	}
	return snapshot, nil
}

type Recovery struct {
	WorkerStopped     bool
	CapacitySafe      bool
	EvidenceReference string
}

// Resume reattaches a trusted host to its admitted ticket across workflow steps.
// A persisted report is not authorization: the host must bind it to its current
// execution identity. Recovery-required entries need Recover, not this API.
func (s *Scheduler) Resume(ctx context.Context, report Report) (*Session, error) {
	if s == nil || report.Pool != s.Pool || report.Capacity != s.Capacity || report.Status != "admitted" {
		return nil, ErrOwnership
	}
	state, err := s.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range state.Entries {
		if entry.Owner == report.Owner && entry.Request == report.Request && entry.Worker == report.Worker && entry.Sequence == report.Sequence && entry.Status == "running" {
			return &Session{s, entry}, nil
		}
	}
	return nil, ErrOwnership
}

// Recover requires explicit host checks. Timeouts, absent heartbeats and elapsed
// time are not proof that a worker stopped or its capacity is safe to reuse.
func (s *Scheduler) Recover(ctx context.Context, owner string, proof Recovery) (Entry, error) {
	var recovered Entry
	if !proof.WorkerStopped || !proof.CapacitySafe || !identifier(proof.EvidenceReference, 512) {
		return recovered, ErrOwnership
	}
	err := s.transaction(ctx, func(state *State) error {
		for i, entry := range state.Entries {
			if entry.Owner == owner {
				recovered = entry
				state.Entries = slices.Delete(state.Entries, i, i+1)
				return nil
			}
		}
		return ErrOwnership
	})
	return recovered, err
}
