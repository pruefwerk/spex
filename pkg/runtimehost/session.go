package runtimehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type cleanupReceipt struct {
	Schema string `json:"schema"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	Scope  string `json:"scope"`
}

// Session persists admission mode once and checks it between sequential steps.
// ScheduledOperation must confirm durable admission/check/release. Cleanup must
// verify ownership; the SDK never guesses which resources it may delete.
type Session struct {
	Identity           Identity
	Directory          string
	CleanupSchema      string
	ScheduledOperation func(context.Context, Operation, string) error
	Cleanup            func(context.Context) error
}

func (s Session) Gate(ctx context.Context, operation Operation) error {
	if operation != Acquire && operation != Check && operation != Finish {
		return ErrOperation
	}
	relative, err := filepath.Rel(s.Identity.Root, s.Directory)
	if !s.Identity.valid() || !filepath.IsAbs(s.Directory) || err != nil ||
		relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		s.CleanupSchema == "" {
		return ErrIdentity
	}
	if operation != Finish && ctx.Err() != nil {
		return ctx.Err()
	}
	identity := s.Identity
	path := filepath.Join(s.Directory, "context.json")
	if operation == Acquire {
		if err := os.MkdirAll(filepath.Dir(s.Directory), 0700); err != nil {
			return ErrPersistence
		}
		if err := os.Mkdir(s.Directory, 0700); err != nil {
			return ErrPersistence
		}
		if createPrivate(path, identity) != nil {
			return ErrPersistence
		}
	} else {
		var recorded Identity
		if err := readPrivate(path, &recorded); err != nil {
			if operation == Finish && os.IsNotExist(err) && s.Cleanup != nil {
				return s.Cleanup(ctx)
			}
			return ErrIdentity
		}
		if !recorded.valid() {
			return ErrIdentity
		}
		// Cleanup retains recorded admission mode even when credentials disappear.
		if operation == Finish {
			identity.Mode = recorded.Mode
		}
		if identity != recorded {
			return ErrIdentity
		}
	}
	marker := filepath.Join(s.Directory, "cleanup.json")
	if operation == Check {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			return ErrCleanup
		}
	}
	if identity.Mode == "enabled" {
		var operationErr error
		if s.ScheduledOperation != nil {
			operationErr = s.ScheduledOperation(ctx, operation, path)
		}
		if s.ScheduledOperation == nil || operationErr != nil {
			if operation == Finish && s.Cleanup != nil {
				operationErr = errors.Join(operationErr, s.Cleanup(ctx))
			}
			return retainCause(ErrAdmission, operationErr)
		}
	} else if operation == Finish {
		if s.Cleanup == nil {
			return ErrCleanup
		}
		if err := s.Cleanup(ctx); err != nil {
			return retainCause(ErrCleanup, err)
		}
	}
	if operation == Finish {
		expected := cleanupReceipt{s.CleanupSchema, identity.Mode, "succeeded", identity.ScopeName}
		if _, err := os.Lstat(marker); os.IsNotExist(err) {
			return createPrivate(marker, expected)
		}
		var recorded cleanupReceipt
		if readPrivate(marker, &recorded) != nil || recorded != expected {
			return ErrIdentity
		}
	}
	return nil
}
