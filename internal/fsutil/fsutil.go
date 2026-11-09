// Package fsutil reads and writes provider files durably: stable reads of files
// another process may append to, exclusive creation, and atomic replacement.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StageSuffix names the file written next to a destination before it is
// renamed into place.
const StageSuffix = ".agent-teleport-stage"

// ReadStable reads a regular file and fails when it changed during the read.
func ReadStable(path string) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("%s changed while it was being read", path)
	}
	return data, nil
}

// WriteExclusive creates path with data and syncs it, failing if path exists.
func WriteExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// WriteAtomic replaces path with data through a synced stage file and a
// rename, so readers see either the old or the new content. A stage file left
// by an interrupted write is discarded.
func WriteAtomic(path string, data []byte) error {
	stage := path + StageSuffix
	if err := RemoveIfExists(stage); err != nil {
		return err
	}
	if err := WriteExclusive(stage, data); err != nil {
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		return errors.Join(err, RemoveIfExists(stage))
	}
	return SyncDirectory(filepath.Dir(path))
}

// SyncDirectory makes the entries of a directory durable.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	if closeErr := directory.Close(); syncErr == nil {
		syncErr = closeErr
	}
	return syncErr
}

// RemoveIfExists removes path and succeeds when it is already absent.
func RemoveIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
