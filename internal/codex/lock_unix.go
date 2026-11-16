//go:build unix

package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockThread takes the writer lock that Codex holds on thread-writer-locks/<id>.lock
// while a process has the thread loaded. It fails with errSessionOpen instead
// of waiting.
func lockThread(home, thread string) (func(), error) {
	return threadLock(home, thread, true)
}

func checkThreadClosed(home, thread string) error {
	unlock, err := threadLock(home, thread, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	unlock()
	return nil
}

func threadLock(home, thread string, create bool) (func(), error) {
	directory := filepath.Join(home, "thread-writer-locks")
	if create {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create Codex lock directory: %w", err)
		}
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(filepath.Join(directory, thread+".lock"), flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open Codex thread lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("session %s: %w", thread, errSessionOpen)
		}
		return nil, fmt.Errorf("lock Codex thread: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
