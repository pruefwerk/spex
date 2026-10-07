// Package resourceclaims coordinates access to runtime-resolved resources.
// Runtimes choose physical identities and a shared Store. Claims coordinate
// participating executions; they do not confer ownership of those resources.
package resourceclaims

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

type Access string

const (
	Shared    Access = "shared"
	Exclusive Access = "exclusive"
)

type Claim struct {
	Resource string `json:"resource"`
	Access   Access `json:"access"`
}

// Normalize sorts and deduplicates claims, promoting duplicate access to exclusive.
// Resource identities remain case-sensitive; only the runtime can canonicalize them.
func Normalize(input []Claim) ([]Claim, error) {
	if len(input) > 1000 {
		return nil, errors.New("too many resource claims")
	}
	byResource := map[string]Access{}
	for _, claim := range input {
		if claim.Resource == "" || len(claim.Resource) > 512 || strings.IndexFunc(claim.Resource, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || (claim.Access != Shared && claim.Access != Exclusive) {
			return nil, errors.New("invalid resource claim")
		}
		if byResource[claim.Resource] != Exclusive {
			byResource[claim.Resource] = claim.Access
		}
	}
	result := make([]Claim, 0, len(byResource))
	for resource, access := range byResource {
		result = append(result, Claim{resource, access})
	}
	slices.SortFunc(result, func(a, b Claim) int { return strings.Compare(a.Resource, b.Resource) })
	return result, nil
}

type Holding struct {
	Owner            string  `json:"owner"`
	Claims           []Claim `json:"claims"`
	RecoveryRequired bool    `json:"recovery_required"`
}
type State struct {
	Holdings []Holding `json:"holdings"`
}

// Store must atomically commit the complete transaction across all participating
// processes. A callback error must leave state unchanged. Stores may retry the
// callback: it must be deterministic, perform no I/O and not retain the state
// pointer. Backend failures must fail closed.
type Store interface {
	Transaction(context.Context, func(*State) error) error
}

var (
	ErrBusy      = errors.New("resource claims conflict")
	ErrStore     = errors.New("resource coordination unavailable")
	ErrOwnership = errors.New("resource claim ownership unavailable")
)

type Coordinator struct {
	Store        Store
	PollInterval time.Duration
}

type Report struct {
	Owner            string  `json:"owner,omitempty"`
	Claims           []Claim `json:"claims"`
	WaitMilliseconds int64   `json:"wait_ms"`
	Status           string  `json:"status"`
}

type Session struct {
	coordinator *Coordinator
	owner       string
	claims      []Claim
}

// Acquire commits the entire claim set or waits without holding a subset.
// No wall-clock timeout expires an existing owner's claims.
func (c *Coordinator) Acquire(ctx context.Context, requested []Claim) (*Session, Report, error) {
	claims, err := Normalize(requested)
	report := Report{Claims: claims, Status: "not_acquired"}
	if err != nil {
		return nil, report, err
	}
	if len(claims) == 0 {
		report.Status = "not_requested"
		return nil, report, nil
	}
	if c == nil || c.Store == nil || c.PollInterval < 0 {
		return nil, report, ErrStore
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, report, ErrStore
	}
	owner := hex.EncodeToString(nonce[:])
	report.Owner = owner
	started := time.Now()
	interval := c.PollInterval
	if interval == 0 {
		interval = 100 * time.Millisecond
	}
	requestedAccess := map[string]Access{}
	for _, claim := range claims {
		requestedAccess[claim.Resource] = claim.Access
	}
	for {
		if err := ctx.Err(); err != nil {
			report.WaitMilliseconds = time.Since(started).Milliseconds()
			return nil, report, err
		}
		err := c.Store.Transaction(ctx, func(state *State) error {
			for _, holding := range state.Holdings {
				for _, active := range holding.Claims {
					if access, requested := requestedAccess[active.Resource]; requested && (holding.RecoveryRequired || active.Access == Exclusive || access == Exclusive) {
						return ErrBusy
					}
				}
			}
			state.Holdings = append(state.Holdings, Holding{Owner: owner, Claims: slices.Clone(claims)})
			return nil
		})
		report.WaitMilliseconds = time.Since(started).Milliseconds()
		if err == nil {
			report.Owner, report.Status = owner, "acquired"
			return &Session{c, owner, slices.Clone(claims)}, report, nil
		}
		if !errors.Is(err, ErrBusy) {
			report.Status = "unknown"
			if ctx.Err() != nil {
				return nil, report, ctx.Err()
			}
			return nil, report, ErrStore
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			report.WaitMilliseconds = time.Since(started).Milliseconds()
			return nil, report, ctx.Err()
		case <-timer.C:
		}
	}
}

// Inspect returns a detached snapshot without committing a write.
func (c *Coordinator) Inspect(ctx context.Context) ([]Holding, error) {
	if c == nil || c.Store == nil {
		return nil, ErrStore
	}
	var snapshot State
	readOnly := errors.New("read-only claim inspection")
	err := c.Store.Transaction(ctx, func(state *State) error { snapshot = clone(*state); return readOnly })
	if !errors.Is(err, readOnly) {
		return nil, ErrStore
	}
	return snapshot.Holdings, nil
}

// Finish releases claims only when the runtime verifies safe access for successors.
// Otherwise it leaves the complete holding quarantined for explicit reconciliation.
func (s *Session) Finish(ctx context.Context, safe bool) error {
	if s == nil {
		return nil
	}
	err := s.coordinator.Store.Transaction(ctx, func(state *State) error {
		for index, holding := range state.Holdings {
			if holding.Owner != s.owner {
				continue
			}
			if !slices.Equal(holding.Claims, s.claims) {
				return ErrOwnership
			}
			if safe {
				state.Holdings = slices.Delete(state.Holdings, index, index+1)
			} else {
				state.Holdings[index].RecoveryRequired = true
			}
			return nil
		}
		return ErrOwnership
	})
	if err != nil {
		return ErrStore
	}
	return nil
}

type Recovery struct {
	OwnerStopped      bool
	ResourcesSafe     bool
	EvidenceReference string
}

// Recover is a trusted-host operation after checking worker termination and the
// runtime's resource postconditions. It must never be driven by elapsed time alone.
// Return the cleared holding so the host can retain its reconciliation evidence.
func (c *Coordinator) Recover(ctx context.Context, owner string, recovery Recovery) (Holding, error) {
	var cleared Holding
	if c == nil || c.Store == nil || !recovery.OwnerStopped || !recovery.ResourcesSafe || strings.TrimSpace(recovery.EvidenceReference) == "" {
		return cleared, errors.New("resource recovery requires verified termination, safe resources and evidence")
	}
	err := c.Store.Transaction(ctx, func(state *State) error {
		for index, holding := range state.Holdings {
			if holding.Owner == owner {
				cleared = holding
				state.Holdings = slices.Delete(state.Holdings, index, index+1)
				return nil
			}
		}
		return ErrOwnership
	})
	if err != nil {
		return Holding{}, ErrStore
	}
	return cleared, nil
}

func clone(state State) State {
	copy := State{Holdings: slices.Clone(state.Holdings)}
	for i := range copy.Holdings {
		copy.Holdings[i].Claims = slices.Clone(copy.Holdings[i].Claims)
	}
	return copy
}

func validate(state State) error {
	if len(state.Holdings) > 100000 {
		return ErrStore
	}
	owners := map[string]bool{}
	for _, holding := range state.Holdings {
		owner, err := hex.DecodeString(holding.Owner)
		claims, claimErr := Normalize(holding.Claims)
		if err != nil || len(owner) != 16 || owners[holding.Owner] || claimErr != nil || len(claims) == 0 || !slices.Equal(claims, holding.Claims) {
			return ErrStore
		}
		owners[holding.Owner] = true
	}
	return nil
}
