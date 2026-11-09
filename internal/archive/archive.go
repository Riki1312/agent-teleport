// Package archive reads and writes provider-neutral session archives.
//
// An archive is a gzip-compressed tar file whose first entry is manifest.json.
// The manifest identifies the session and lists every payload file with its size
// and SHA-256 digest; the payload files follow in manifest order.
package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Format identifies agent-teleport archives in their manifest.
const Format = "agent-teleport"

// FormatVersion is the only manifest version this build reads and writes.
const FormatVersion = 1

// Extension is the file extension of packed archives.
const Extension = ".teleport"

const (
	manifestPath    = "manifest.json"
	maxManifestSize = 1 << 20
	maxPayloadSize  = 512 << 20
	maxTotalSize    = 1 << 30
	maxPayloadFiles = 4096
)

// Manifest describes the session stored in an archive.
type Manifest struct {
	Format          string `json:"format"`
	FormatVersion   int    `json:"format_version"`
	Provider        string `json:"provider"`
	ProviderVersion string `json:"provider_version"`
	SessionID       string `json:"session_id"`
	Workspace       string `json:"workspace"`
	// Home is the directory where the provider stored the session on the
	// source machine. Providers that record paths into it move them on import.
	Home      string    `json:"home"`
	CreatedAt time.Time `json:"created_at"`
	Files     []File    `json:"files"`
}

// File describes one payload file listed in a manifest.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Header holds the session identity a provider supplies when creating an
// archive.
type Header struct {
	Provider        string
	ProviderVersion string
	SessionID       string
	Workspace       string
	Home            string
	CreatedAt       time.Time
}

// Payload is the content of one provider file, addressed by a slash-separated
// relative path.
type Payload struct {
	Path string
	Data []byte
}

// Archive is a validated manifest together with the payload files it lists.
type Archive struct {
	Manifest Manifest
	Payloads []Payload
}

// New validates header and payloads and builds the manifest that describes
// them.
func New(header Header, payloads []Payload) (Archive, error) {
	manifest := Manifest{
		Format:          Format,
		FormatVersion:   FormatVersion,
		Provider:        header.Provider,
		ProviderVersion: header.ProviderVersion,
		SessionID:       header.SessionID,
		Workspace:       header.Workspace,
		Home:            header.Home,
		CreatedAt:       header.CreatedAt.UTC().Truncate(time.Second),
		Files:           make([]File, 0, len(payloads)),
	}
	for _, payload := range payloads {
		digest := sha256.Sum256(payload.Data)
		manifest.Files = append(manifest.Files, File{
			Path:   payload.Path,
			Size:   int64(len(payload.Data)),
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	if err := validateManifest(manifest); err != nil {
		return Archive{}, err
	}
	return Archive{Manifest: manifest, Payloads: payloads}, nil
}

// Data returns the content of the payload file at path.
func (a Archive) Data(path string) ([]byte, bool) {
	for _, payload := range a.Payloads {
		if payload.Path == path {
			return payload.Data, true
		}
	}
	return nil, false
}

// Encode returns the packed archive. It decodes the result before returning,
// so a caller never writes an archive that this build cannot read back.
func (a Archive) Encode() ([]byte, error) {
	if err := validateManifest(a.Manifest); err != nil {
		return nil, err
	}
	manifestJSON, err := json.MarshalIndent(a.Manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	entries := append([]Payload{{Path: manifestPath, Data: append(manifestJSON, '\n')}}, a.Payloads...)

	var encoded bytes.Buffer
	gzipWriter := gzip.NewWriter(&encoded)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.Path,
			Mode:     0o600,
			Size:     int64(len(entry.Data)),
			ModTime:  a.Manifest.CreatedAt,
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write %s header: %w", entry.Path, err)
		}
		if _, err := tarWriter.Write(entry.Data); err != nil {
			return nil, fmt.Errorf("write %s: %w", entry.Path, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("finish tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("finish gzip archive: %w", err)
	}
	if _, err := Decode(bytes.NewReader(encoded.Bytes())); err != nil {
		return nil, fmt.Errorf("verify encoded archive: %w", err)
	}
	return encoded.Bytes(), nil
}

// WriteFile writes the packed archive to a new owner-only file at path. It never
// replaces an existing file and removes a partially written one. It avoids hard
// links, which removable FAT and exFAT drives do not support.
func (a Archive) WriteFile(path string) error {
	encoded, err := a.Encode()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("output already exists: %s", path)
	}
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	_, err = file.Write(encoded)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

// Decode reads and fully validates a packed archive. It accepts only the
// manifest followed by exactly the regular files it lists, in order, within
// size limits and with matching digests.
func Decode(input io.Reader) (Archive, error) {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return Archive{}, fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)

	manifestJSON, err := readEntry(tarReader, manifestPath, maxManifestSize)
	if err != nil {
		return Archive{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Archive{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Archive{}, err
	}
	payloads := make([]Payload, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		data, err := readEntry(tarReader, file.Path, file.Size)
		if err != nil {
			return Archive{}, err
		}
		if int64(len(data)) != file.Size {
			return Archive{}, fmt.Errorf("archive entry %q has size %d, manifest lists %d", file.Path, len(data), file.Size)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return Archive{}, fmt.Errorf("archive entry %q does not match its checksum", file.Path)
		}
		payloads = append(payloads, Payload{Path: file.Path, Data: data})
	}
	if header, err := tarReader.Next(); err != io.EOF {
		if err != nil {
			return Archive{}, fmt.Errorf("read trailing archive entry: %w", err)
		}
		return Archive{}, fmt.Errorf("archive contains unlisted entry %q", header.Name)
	}
	return Archive{Manifest: manifest, Payloads: payloads}, nil
}

func readEntry(reader *tar.Reader, name string, limit int64) ([]byte, error) {
	header, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", name, err)
	}
	if header.Name != name {
		return nil, fmt.Errorf("archive entry is %q, want %q", header.Name, name)
	}
	if header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("archive entry %q is not a regular file", name)
	}
	if header.Size < 0 || header.Size > limit {
		return nil, fmt.Errorf("archive entry %q exceeds its size limit", name)
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", name, err)
	}
	if int64(len(data)) != header.Size {
		return nil, fmt.Errorf("archive entry %q has an inconsistent size", name)
	}
	return data, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Format != Format {
		return fmt.Errorf("unsupported archive format %q", manifest.Format)
	}
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported archive format version %d", manifest.FormatVersion)
	}
	if manifest.Provider == "" || manifest.ProviderVersion == "" || manifest.SessionID == "" {
		return errors.New("manifest is missing provider, provider version, or session ID")
	}
	if manifest.CreatedAt.IsZero() {
		return errors.New("manifest is missing its creation time")
	}
	if !filepath.IsAbs(manifest.Workspace) || !filepath.IsAbs(manifest.Home) {
		return errors.New("manifest workspace and home must be absolute paths")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxPayloadFiles {
		return fmt.Errorf("manifest must list between 1 and %d payload files", maxPayloadFiles)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	var total int64
	for _, file := range manifest.Files {
		if err := validatePayloadPath(file.Path); err != nil {
			return err
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return fmt.Errorf("manifest lists payload %q twice", file.Path)
		}
		seen[file.Path] = struct{}{}
		if file.Size < 0 || file.Size > maxPayloadSize {
			return fmt.Errorf("payload %q exceeds its size limit", file.Path)
		}
		total += file.Size
		if digest, err := hex.DecodeString(file.SHA256); err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("payload %q has an invalid SHA-256 digest", file.Path)
		}
	}
	if total > maxTotalSize {
		return errors.New("archive payload exceeds its total size limit")
	}
	return nil
}

// validatePayloadPath accepts only clean, relative, slash-separated paths so a
// payload can never address a location outside its session directory.
func validatePayloadPath(path string) error {
	if !fs.ValidPath(path) || path == "." || path == manifestPath || strings.Contains(path, `\`) {
		return fmt.Errorf("invalid payload path %q", path)
	}
	return nil
}
