//go:build linux || darwin

package resourceclaims

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileStore coordinates separate processes on one host using a durable local
// directory and OS advisory locking. Do not use this adapter on NFS or separate
// ephemeral GitHub runners; those require a shared transactional service adapter.
type FileStore struct{ directory string }

func NewFileStore(directory string) (*FileStore, error) {
	path, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrStore
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, ErrStore
	}
	return &FileStore{path}, nil
}

func (s *FileStore) Transaction(ctx context.Context, transaction func(*State) error) error {
	lock, err := os.OpenFile(filepath.Join(s.directory, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ErrStore
	}
	defer lock.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return ErrStore
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := State{}
	file, err := os.Open(filepath.Join(s.directory, "claims.json"))
	if err == nil {
		info, statErr := file.Stat()
		if statErr != nil || info.Size() > 64<<20 {
			file.Close()
			return ErrStore
		}
		decoder := json.NewDecoder(io.LimitReader(file, 64<<20))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&state)
		var extra any
		if err == nil && decoder.Decode(&extra) != io.EOF {
			err = ErrStore
		}
		file.Close()
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil || validate(state) != nil {
		return ErrStore
	}
	if err := transaction(&state); err != nil {
		return err
	}
	if validate(state) != nil {
		return ErrStore
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.directory, "claims-*.pending")
	if err != nil {
		return ErrStore
	}
	defer os.Remove(temporary.Name())
	err = json.NewEncoder(temporary).Encode(state)
	if err == nil {
		info, statErr := temporary.Stat()
		if statErr != nil || info.Size() > 64<<20 {
			err = ErrStore
		}
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil || closeErr != nil {
		return ErrStore
	}
	if err := os.Rename(temporary.Name(), filepath.Join(s.directory, "claims.json")); err != nil {
		return ErrStore
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return ErrStore
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return ErrStore
	}
	return nil
}
