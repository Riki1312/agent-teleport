package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Riki1312/agent-teleport/internal/fsutil"
)

// Suffixes of the hidden sibling directories that WriteDir uses to replace an
// unpacked archive in one rename.
const (
	stageDirSuffix    = ".agent-teleport-stage"
	previousDirSuffix = ".agent-teleport-previous"
)

// ReadDir reads and fully validates an unpacked archive: a directory holding
// manifest.json and exactly the regular files it lists, at their payload
// paths, with matching sizes and digests.
func ReadDir(dir string) (Archive, error) {
	manifestJSON, err := readRegular(filepath.Join(dir, manifestPath), maxManifestSize)
	if err != nil {
		return Archive{}, err
	}
	manifest, err := decodeManifest(manifestJSON)
	if err != nil {
		return Archive{}, err
	}
	listed := map[string]bool{manifestPath: true}
	payloads := make([]Payload, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		data, err := readRegular(filepath.Join(dir, filepath.FromSlash(file.Path)), file.Size)
		if err != nil {
			return Archive{}, err
		}
		if err := verifyPayload(file, data); err != nil {
			return Archive{}, err
		}
		listed[file.Path] = true
		payloads = append(payloads, Payload{Path: file.Path, Data: data})
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !listed[filepath.ToSlash(relative)] {
			return fmt.Errorf("archive directory contains unlisted file %q", filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return Archive{}, err
	}
	return Archive{Manifest: manifest, Payloads: payloads}, nil
}

// WriteDir replaces the unpacked archive at dir, creating it if needed. The
// archive is written to a sibling directory whose name starts with a dot and
// verified, then renamed into place, so readers see either the old or the new
// archive.
func (a Archive) WriteDir(dir string) error {
	if err := validateManifest(a.Manifest); err != nil {
		return err
	}
	if err := RecoverDir(dir); err != nil {
		return err
	}
	stage, previous := siblingDirs(dir)
	if err := a.writeFiles(stage); err != nil {
		return errors.Join(err, os.RemoveAll(stage))
	}
	written, err := ReadDir(stage)
	if err == nil && !written.equal(a) {
		err = errors.New("written archive does not match")
	}
	if err != nil {
		return errors.Join(fmt.Errorf("verify archive directory: %w", err), os.RemoveAll(stage))
	}
	if err := os.Rename(dir, previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(err, os.RemoveAll(stage))
	}
	if err := os.Rename(stage, dir); err != nil {
		return err
	}
	if err := os.RemoveAll(previous); err != nil {
		return err
	}
	return fsutil.SyncDirectory(filepath.Dir(dir))
}

// RecoverDir completes or undoes a WriteDir of dir that was interrupted: a
// previous archive moved aside is restored unless the new one is in place,
// and a partial stage directory is removed.
func RecoverDir(dir string) error {
	stage, previous := siblingDirs(dir)
	if _, err := os.Stat(previous); err == nil {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			if err := os.Rename(previous, dir); err != nil {
				return fmt.Errorf("restore interrupted archive write: %w", err)
			}
		} else if err := os.RemoveAll(previous); err != nil {
			return err
		}
	}
	return os.RemoveAll(stage)
}

func siblingDirs(dir string) (string, string) {
	parent, name := filepath.Split(filepath.Clean(dir))
	return filepath.Join(parent, "."+name+stageDirSuffix), filepath.Join(parent, "."+name+previousDirSuffix)
}

func (a Archive) writeFiles(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	manifestJSON, err := encodeManifest(a.Manifest)
	if err != nil {
		return err
	}
	entries := append([]Payload{{Path: manifestPath, Data: manifestJSON}}, a.Payloads...)
	for _, entry := range entries {
		path := filepath.Join(dir, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := fsutil.WriteExclusive(path, entry.Data); err != nil {
			return err
		}
	}
	return nil
}

func (a Archive) equal(other Archive) bool {
	if len(a.Payloads) != len(other.Payloads) {
		return false
	}
	for index, payload := range a.Payloads {
		if payload.Path != other.Payloads[index].Path || !bytes.Equal(payload.Data, other.Payloads[index].Data) {
			return false
		}
	}
	return true
}

// readRegular reads a regular file of at most limit bytes, refusing links.
func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds its size limit", path)
	}
	return os.ReadFile(path)
}

func encodeManifest(manifest Manifest) ([]byte, error) {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func verifyPayload(file File, data []byte) error {
	if int64(len(data)) != file.Size {
		return fmt.Errorf("payload %q has size %d, manifest lists %d", file.Path, len(data), file.Size)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != file.SHA256 {
		return fmt.Errorf("payload %q does not match its checksum", file.Path)
	}
	return nil
}
