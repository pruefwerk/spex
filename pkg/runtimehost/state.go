// Package runtimehost supplies durable lifecycle checkpoints for trusted hosts
// whose execution spans multiple processes or CI steps. Hosts retain ownership
// of environment validation, resource cleanup and scheduling configuration.
package runtimehost

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrIdentity    = errors.New("execution checkpoint identity invalid or changed")
	ErrOperation   = errors.New("unsupported execution checkpoint operation")
	ErrAdmission   = errors.New("capacity operation unconfirmed; inspect scheduling evidence")
	ErrPersistence = errors.New("cannot persist capacity evidence; inspect queue before recovery")
	ErrCleanup     = errors.New("owned cleanup or capacity release unconfirmed")
)

type Operation string

const (
	Acquire Operation = "acquire"
	Check   Operation = "check"
	Finish  Operation = "finish"
)

// Identity uses host-selected schema and scope names. Its JSON encoding preserves
// existing checkpoint formats; the SDK does not allocate project scope identities.
type Identity struct {
	Schema    string `json:"schema"`
	Root      string `json:"root"`
	ScopeName string `json:"scope_name"`
	ScopePath string `json:"scope_path"`
	Worker    string `json:"worker"`
	Request   string `json:"request"`
	Mode      string `json:"mode"`
}

func (i Identity) valid() bool {
	return i.Schema != "" && filepath.IsAbs(i.Root) && filepath.IsAbs(i.ScopePath) &&
		i.ScopeName != "" && i.Worker != "" && i.Request != "" &&
		(i.Mode == "enabled" || i.Mode == "disabled")
}

func readPrivate(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ErrIdentity
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return ErrIdentity
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrIdentity
	}
	return nil
}

func ReadIdentity(path string) (Identity, error) {
	var value Identity
	if readPrivate(path, &value) != nil || !value.valid() {
		return value, ErrIdentity
	}
	return value, nil
}

func createPrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrPersistence
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func replacePrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrPersistence
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".capacity-*")
	if err != nil {
		return ErrPersistence
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return ErrPersistence
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return ErrPersistence
	}
	if err := file.Close(); err != nil {
		return ErrPersistence
	}
	if os.Rename(file.Name(), path) != nil {
		return ErrPersistence
	}
	return nil
}
