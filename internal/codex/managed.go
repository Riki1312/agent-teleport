package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// managedStore keeps native storage rules in Store and routes explicit local
// references to managed isolated histories. Native IDs still name the current
// Codex home; archives always retain their native identities.
type managedStore struct{ primary *Store }

func (m *managedStore) Close() { m.primary.Close() }

func (m *managedStore) lookup(reference string) (isolatedImport, string, error) {
	parts := strings.SplitN(reference, ":", 2)
	if len(parts) == 2 && !validSessionID(parts[1]) {
		return isolatedImport{}, "", errors.New("invalid isolated session reference")
	}
	entries, err := isolationEntries(m.primary.baseHome())
	if err != nil {
		return isolatedImport{}, "", err
	}
	var found []isolatedImport
	for _, entry := range entries {
		if entry.Ready && entry.reference() == parts[0] {
			found = append(found, entry)
		}
	}
	if len(found) != 1 {
		return isolatedImport{}, "", fmt.Errorf("isolated reference %q matches %d imports", reference, len(found))
	}
	id := found[0].SessionID
	if len(parts) == 2 {
		id = parts[1]
	}
	return found[0], id, nil
}

func (m *managedStore) Sessions(workspace string) ([]provider.Session, error) {
	sessions, err := m.primary.Sessions(workspace)
	if err != nil {
		return sessions, err
	}
	if m.primary.isolation != nil {
		for index := range sessions {
			sessions[index].Reference = m.primary.isolation.referenceFor(sessions[index].ID)
		}
		return sessions, nil
	}
	entries, err := isolationEntries(m.primary.home)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Ready || (workspace != "" && entry.Workspace != workspace) {
			continue
		}
		store := m.primary.isolatedStore(entry)
		session, err := store.Session(entry.SessionID)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.reference(), err)
		}
		session.Reference = entry.reference()
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (m *managedStore) Session(reference string) (provider.Session, error) {
	if !strings.HasPrefix(reference, "iso-") {
		session, err := m.primary.Session(reference)
		if m.primary.isolation != nil {
			session.Reference = m.primary.isolation.referenceFor(session.ID)
		}
		return session, err
	}
	i, id, err := m.lookup(reference)
	if err != nil {
		return provider.Session{}, err
	}
	session, err := m.primary.isolatedStore(i).Session(id)
	session.Reference = reference
	return session, err
}

func (m *managedStore) Export(reference string, created time.Time) (archive.Archive, error) {
	if !strings.HasPrefix(reference, "iso-") {
		return m.primary.Export(reference, created)
	}
	i, id, err := m.lookup(reference)
	if err != nil {
		return archive.Archive{}, err
	}
	return m.primary.isolatedStore(i).Export(id, created)
}

func (m *managedStore) ResumeCommand(reference, workspace string) (*exec.Cmd, error) {
	if !strings.HasPrefix(reference, "iso-") {
		return m.primary.ResumeCommand(reference, workspace)
	}
	i, id, err := m.lookup(reference)
	if err != nil {
		return nil, err
	}
	if err := prepareIsolation(i); err != nil {
		return nil, err
	}
	return m.primary.isolatedStore(i).ResumeCommand(id, workspace)
}

func (m *managedStore) Import(request provider.ImportRequest) (provider.ImportResult, error) {
	if !request.Isolated {
		result, err := m.primary.Import(request)
		if err == nil && m.primary.isolation != nil {
			result.Reference = m.primary.isolation.referenceFor(result.SessionID)
		}
		return result, err
	}
	i := isolatedImport{Version: 1, BaseHome: m.primary.baseHome(), SessionID: request.Archive.Manifest.SessionID, Workspace: request.Workspace}
	if !validSessionID(i.SessionID) {
		return provider.ImportResult{}, errors.New("invalid isolated session ID")
	}
	entries, err := isolationEntries(i.BaseHome)
	if err != nil {
		return provider.ImportResult{}, err
	}
	for _, entry := range entries {
		if entry.reference() == i.reference() {
			if entry.key() != i.key() {
				return provider.ImportResult{}, errors.New("isolated import reference collision")
			}
			i = entry
		}
	}
	store := m.primary.isolatedStore(i)
	defer store.Close()
	request.Isolated = false
	// Validate and relocate once before creating storage. Existing imports
	// must recover interrupted native writes before their mutation plan.
	session, err := store.prepareSession(request.Archive, request.Workspace)
	if err != nil {
		return provider.ImportResult{}, err
	}
	if request.DryRun {
		result, err := store.importSession(session, request)
		result.Reference = i.reference()
		return result, err
	}
	result := provider.ImportResult{}
	if err := privateDirectory(i.home()); err != nil {
		return result, err
	}
	unlock, err := lockThread(i.home(), "agent-teleport-import")
	if errors.Is(err, errSessionOpen) {
		return result, errors.New("another agent-teleport import is updating this isolated copy; retry when it finishes")
	}
	if err != nil {
		return result, fmt.Errorf("lock isolated import: %w", err)
	}
	defer unlock()
	if current, err := readIsolation(i.home()); err == nil {
		i = current
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err := prepareIsolation(i); err != nil {
		return result, err
	}
	result, err = store.importSession(session, request)
	if err != nil {
		return result, err
	}
	i.Ready = true
	if err := writeIsolation(i); err != nil {
		return result, fmt.Errorf("commit isolated import receipt: %w", err)
	}
	result.Reference = i.reference()
	return result, nil
}
