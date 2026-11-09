package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/fsutil"
)

// appendSuffix names the marker kept next to a rollout while records are
// appended to it.
const appendSuffix = ".agent-teleport-append"

// errSessionOpen reports that a Codex process has a thread loaded.
var errSessionOpen = errors.New("the session is open in Codex; close it and retry")

// appendRecords appends complete records to the rollout of thread at path,
// which must still have the size the append was planned against. Codex picks
// up appended records when it next loads the thread. The thread's writer lock
// is held so that no Codex process writes concurrently, and a marker records
// the original and final sizes until the append is durable.
func appendRecords(home, thread, path string, size int64, records []byte) error {
	unlock, err := lockThread(home, thread)
	if err != nil {
		return err
	}
	defer unlock()
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat rollout: %w", err)
	}
	if info.Size() != size {
		return fmt.Errorf("rollout %s changed while the import was planned; import again", path)
	}
	marker := path + appendSuffix
	sizes := fmt.Sprintf("%d %d\n", info.Size(), info.Size()+int64(len(records)))
	if err := fsutil.WriteExclusive(marker, []byte(sizes)); err != nil {
		return fmt.Errorf("record append marker: %w", err)
	}
	if err := fsutil.SyncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open rollout for append: %w", err)
	}
	_, err = file.Write(records)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("append records: %w", err)
	}
	if err := os.Remove(marker); err != nil {
		return fmt.Errorf("commit append: %w", err)
	}
	return fsutil.SyncDirectory(filepath.Dir(path))
}

// recoverAppend completes or undoes an append to the rollout at path that was
// interrupted before its marker was removed.
func recoverAppend(home, thread, path string) error {
	marker := path + appendSuffix
	content, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read append marker: %w", err)
	}
	fields := strings.Fields(string(content))
	if len(fields) != 2 {
		return fmt.Errorf("append marker %s is malformed", marker)
	}
	original, originalErr := strconv.ParseInt(fields[0], 10, 64)
	final, finalErr := strconv.ParseInt(fields[1], 10, 64)
	if originalErr != nil || finalErr != nil || original > final {
		return fmt.Errorf("append marker %s is malformed", marker)
	}
	unlock, err := lockThread(home, thread)
	if err != nil {
		return err
	}
	defer unlock()
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat rollout: %w", err)
	}
	switch size := info.Size(); {
	case size == final:
	case size >= original && size < final:
		if err := os.Truncate(path, original); err != nil {
			return fmt.Errorf("undo interrupted append: %w", err)
		}
	default:
		return fmt.Errorf("rollout %s changed after an interrupted append; inspect it before importing again", path)
	}
	if err := os.Remove(marker); err != nil {
		return fmt.Errorf("remove append marker: %w", err)
	}
	return fsutil.SyncDirectory(filepath.Dir(path))
}

// requireClosed fails when a Codex process has thread loaded.
func requireClosed(home, thread string) error {
	unlock, err := lockThread(home, thread)
	if err != nil {
		return err
	}
	unlock()
	return nil
}
