package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/fsutil"
)

const isolationMarker = ".agent-teleport.json"

// isolatedImport is a local receipt, never part of an exported archive.
// Its identity is independent of the archive revision, so updates find the
// same history. Ready is committed only after native import verification.
type isolatedImport struct {
	Version   int    `json:"version"`
	BaseHome  string `json:"base_home"`
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace"`
	Ready     bool   `json:"ready"`
}

func (i isolatedImport) key() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(Provider+"\x00"+i.SessionID+"\x00"+i.Workspace)))
}

func (i isolatedImport) reference() string { return "iso-" + i.key()[:16] }

func (i isolatedImport) referenceFor(id string) string {
	if id == i.SessionID {
		return i.reference()
	}
	return i.reference() + ":" + id
}

func (i isolatedImport) home() string {
	return filepath.Join(i.BaseHome, "agent-teleport", "imports", i.key(), "codex")
}

func (s *Store) baseHome() string {
	if s.isolation != nil {
		return s.isolation.BaseHome
	}
	return s.home
}

func (s *Store) isolatedStore(i isolatedImport) *Store {
	return &Store{ctx: s.ctx, binary: s.binary, home: i.home(), version: s.version, layout: s.layout,
		isolation: &i, runtime: isolationArguments(i.home())}
}

func isolationArguments(home string) []string {
	// Explicit storage overrides outrank inherited CODEX_SQLITE_HOME and
	// user-configured sqlite_home/log_dir; shared configuration must never
	// redirect the isolated process to the primary session databases.
	quoted, _ := json.Marshal(home)
	log, _ := json.Marshal(filepath.Join(home, "log"))
	return []string{"-c", "sqlite_home=" + string(quoted), "-c", "log_dir=" + string(log)}
}

// validateIsolation checks managed requirements before a native process can
// open databases. Codex applies those requirements after CLI overrides, so
// forcing sqlite_home alone does not guarantee an isolated state runtime.
func (s *Store) validateIsolation(workspace string) error {
	if s.isolation == nil {
		return nil
	}
	arguments := append([]string{"app-server", "--print-startup-config"}, s.runtime...)
	command := exec.CommandContext(s.ctx, s.binary, arguments...)
	command.Env = codexEnvironment(s.home)
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		command.Dir = workspace
	}
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("cannot verify isolated Codex storage requirements: %w", err)
	}
	return validateStorageRequirements(data, s.home)
}

func validateStorageRequirements(data []byte, home string) error {
	var startup struct {
		Requirements *struct {
			Values *struct {
				SQLiteHome *string `json:"sqliteHome"`
				LogDir     *string `json:"logDir"`
			} `json:"requirements"`
		} `json:"configRequirements"`
	}
	if err := json.Unmarshal(data, &startup); err != nil {
		return fmt.Errorf("decode native storage requirements: %w", err)
	}
	if startup.Requirements == nil {
		return errors.New("Codex did not report its storage requirements; isolated history cannot be verified")
	}
	if startup.Requirements.Values == nil {
		return nil
	}
	for _, setting := range []struct {
		name     string
		required *string
		expected string
	}{
		{"sqlite_home", startup.Requirements.Values.SQLiteHome, home},
		{"log_dir", startup.Requirements.Values.LogDir, filepath.Join(home, "log")},
	} {
		if setting.required != nil && filepath.Clean(*setting.required) != filepath.Clean(setting.expected) {
			return fmt.Errorf("managed Codex %s requires storage outside this isolated home; import stopped to preserve the existing store", setting.name)
		}
	}
	return nil
}

func codexEnvironment(home string) []string {
	var environment []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			environment = append(environment, value)
		}
	}
	return append(environment, "CODEX_HOME="+home)
}

func readIsolation(home string) (isolatedImport, error) {
	data, err := fsutil.ReadRegular(filepath.Join(home, isolationMarker))
	if err != nil {
		return isolatedImport{}, err
	}
	if len(data) > 64<<10 {
		return isolatedImport{}, errors.New("isolated import receipt is too large")
	}
	var i isolatedImport
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&i); err != nil {
		return i, fmt.Errorf("read isolated import receipt: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return i, errors.New("isolated import receipt has trailing data")
	}
	if i.Version != 1 || !validSessionID(i.SessionID) || !filepath.IsAbs(i.BaseHome) || !filepath.IsAbs(i.Workspace) || filepath.Clean(home) != i.home() {
		return i, errors.New("isolated import receipt does not match its storage location")
	}
	return i, nil
}

func (s *Store) loadIsolation() error {
	i, err := readIsolation(s.home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.isolation, s.runtime = &i, isolationArguments(s.home)
	return nil
}

func writeIsolation(i isolatedImport) error {
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(filepath.Join(i.home(), isolationMarker), append(data, '\n'))
}

func isolationEntries(base string) ([]isolatedImport, error) {
	root := filepath.Join(base, "agent-teleport", "imports")
	paths, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []isolatedImport
	for _, path := range paths {
		key, err := hex.DecodeString(path.Name())
		if err != nil || len(key) != sha256.Size {
			continue
		}
		if !path.IsDir() {
			return nil, fmt.Errorf("isolated import %s is not a directory", path.Name())
		}
		i, err := readIsolation(filepath.Join(root, path.Name(), "codex"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if i.BaseHome != base {
			return nil, errors.New("isolated import belongs to another Codex home")
		}
		entries = append(entries, i)
	}
	return entries, nil
}

// prepareIsolation shares runtime resources by reference, never session
// stores or databases. Native keyring namespaces remain home-specific.
func prepareIsolation(i isolatedImport) error {
	if err := privateDirectory(i.home()); err != nil {
		return err
	}
	exists := false
	if existing, err := readIsolation(i.home()); err == nil {
		exists = true
		if existing.SessionID != i.SessionID || existing.Workspace != i.Workspace || existing.BaseHome != i.BaseHome {
			return errors.New("isolated import receipt changed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	resources := []string{"config.toml", "requirements.toml", "AGENTS.md", "auth.json", ".credentials.json", "skills", "plugins", "rules", "hooks"}
	profiles, err := filepath.Glob(filepath.Join(i.BaseHome, "*.config.toml"))
	if err != nil {
		return err
	}
	for _, path := range profiles {
		resources = append(resources, filepath.Base(path))
	}
	for _, name := range resources {
		source, destination := filepath.Join(i.BaseHome, name), filepath.Join(i.home(), name)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if info, err := os.Lstat(destination); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(destination)
				if err != nil || target != source {
					return fmt.Errorf("isolated runtime resource %s points elsewhere", name)
				}
			}
			// Native configuration or login written locally takes precedence.
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(source, destination); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if !exists {
		return writeIsolation(i)
	}
	return nil
}

// privateDirectory refuses symlinked parents in managed history storage.
func privateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("isolated storage %s is not a real directory", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
