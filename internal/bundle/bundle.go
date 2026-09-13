package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const (
	Format        = "agent-teleport"
	FormatVersion = 1
	ManifestPath  = "manifest.json"
	ChecksumsPath = "checksums.sha256"
	RolloutPath   = "providers/openai-codex/rollout.jsonl"
	maxManifest   = 64 << 10
	maxChecksums  = 4 << 10
	maxRollout    = 512 << 20
)

type Manifest struct {
	Format          string    `json:"format"`
	FormatVersion   int       `json:"format_version"`
	Provider        string    `json:"provider"`
	ProviderVersion string    `json:"provider_version"`
	SessionID       string    `json:"session_id"`
	CreatedAt       time.Time `json:"created_at"`
	OriginalCWD     string    `json:"original_cwd"`
	Payload         struct {
		Rollout string `json:"rollout"`
	} `json:"payload"`
}

type Bundle struct {
	Manifest Manifest
	Rollout  []byte
}

func NewManifest(provider, providerVersion, sessionID, originalCWD string, now time.Time) Manifest {
	manifest := Manifest{
		Format:          Format,
		FormatVersion:   FormatVersion,
		Provider:        provider,
		ProviderVersion: providerVersion,
		SessionID:       sessionID,
		CreatedAt:       now.UTC(),
		OriginalCWD:     originalCWD,
	}
	manifest.Payload.Rollout = RolloutPath
	return manifest
}

func Write(output io.Writer, manifest Manifest, rollout []byte) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	digest := sha256.Sum256(rollout)
	checksums := []byte(hex.EncodeToString(digest[:]) + "  " + RolloutPath + "\n")

	gzipWriter := gzip.NewWriter(output)
	gzipWriter.Header.ModTime = time.Time{}
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name string
		data []byte
	}{
		{ManifestPath, manifestJSON},
		{ChecksumsPath, checksums},
		{RolloutPath, rollout},
	}
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o600,
			Size:     int64(len(entry.data)),
			ModTime:  manifest.CreatedAt,
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write %s header: %w", entry.name, err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			return fmt.Errorf("write %s: %w", entry.name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("finish tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("finish gzip archive: %w", err)
	}
	return nil
}

func Read(input io.Reader) (Bundle, error) {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return Bundle{}, fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	wanted := []struct {
		name string
		max  int64
	}{
		{ManifestPath, maxManifest},
		{ChecksumsPath, maxChecksums},
		{RolloutPath, maxRollout},
	}
	values := make(map[string][]byte, len(wanted))
	for index, expected := range wanted {
		header, err := tarReader.Next()
		if err != nil {
			return Bundle{}, fmt.Errorf("read archive entry %d: %w", index+1, err)
		}
		if header.Name != expected.name {
			return Bundle{}, fmt.Errorf("archive entry %d is %q, want %q", index+1, header.Name, expected.name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return Bundle{}, fmt.Errorf("archive entry %q is not a regular file", header.Name)
		}
		if header.Size < 0 || header.Size > expected.max {
			return Bundle{}, fmt.Errorf("archive entry %q exceeds its size limit", header.Name)
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, expected.max+1))
		if err != nil {
			return Bundle{}, fmt.Errorf("read archive entry %q: %w", header.Name, err)
		}
		if int64(len(data)) != header.Size {
			return Bundle{}, fmt.Errorf("archive entry %q has inconsistent size", header.Name)
		}
		values[header.Name] = data
	}
	if header, err := tarReader.Next(); err != io.EOF {
		if err != nil {
			return Bundle{}, fmt.Errorf("read trailing archive entry: %w", err)
		}
		return Bundle{}, fmt.Errorf("archive contains unexpected entry %q", header.Name)
	}

	var manifest Manifest
	if err := json.Unmarshal(values[ManifestPath], &manifest); err != nil {
		return Bundle{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Bundle{}, err
	}
	if err := verifyChecksum(values[ChecksumsPath], values[RolloutPath]); err != nil {
		return Bundle{}, err
	}
	return Bundle{Manifest: manifest, Rollout: values[RolloutPath]}, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Format != Format {
		return fmt.Errorf("unsupported archive format %q", manifest.Format)
	}
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported archive format version %d", manifest.FormatVersion)
	}
	if manifest.Provider == "" || manifest.ProviderVersion == "" || manifest.SessionID == "" {
		return errors.New("manifest is missing provider, provider version, or session id")
	}
	if manifest.CreatedAt.IsZero() {
		return errors.New("manifest is missing creation time")
	}
	if !filepath.IsAbs(manifest.OriginalCWD) {
		return errors.New("manifest original_cwd must be absolute")
	}
	if manifest.Payload.Rollout != RolloutPath {
		return fmt.Errorf("unsupported rollout payload path %q", manifest.Payload.Rollout)
	}
	return nil
}

func verifyChecksum(checksums, rollout []byte) error {
	line := strings.TrimSuffix(string(checksums), "\n")
	parts := strings.Split(line, "  ")
	if len(parts) != 2 || parts[1] != RolloutPath || len(parts[0]) != sha256.Size*2 {
		return errors.New("checksums.sha256 has an unsupported format")
	}
	wanted, err := hex.DecodeString(parts[0])
	if err != nil {
		return errors.New("checksums.sha256 contains an invalid digest")
	}
	digest := sha256.Sum256(rollout)
	if !bytesEqual(wanted, digest[:]) {
		return errors.New("rollout checksum does not match")
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for index := range a {
		different |= a[index] ^ b[index]
	}
	return different == 0
}
