package ownedkind

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

func writeNew(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func readLedger(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return errors.New("ownership ledger unavailable or invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("ownership ledger unavailable")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (4<<20)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return errors.New("invalid ownership ledger")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid ownership ledger")
	}
	return nil
}
