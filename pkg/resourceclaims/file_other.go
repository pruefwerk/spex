//go:build !linux && !darwin

package resourceclaims

import "context"

type FileStore struct{}

func NewFileStore(string) (*FileStore, error)                            { return nil, ErrStore }
func (*FileStore) Transaction(context.Context, func(*State) error) error { return ErrStore }
