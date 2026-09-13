// Package storage keeps immutable files on disk: issued document PDFs and
// expense attachments. Both are written once, read back only after their
// recorded SHA-256 is re-verified, and covered by the same backups
// (plan.md 14, 18).
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrFileMissing means a stored file is not where the database says it is.
var ErrFileMissing = errors.New("stored file is missing")

// ErrFileCorrupt means a stored file no longer matches its recorded hash.
var ErrFileCorrupt = errors.New("stored file does not match its recorded hash")

// Store keeps immutable files on a filesystem path. A local volume is enough
// for a single-instance deployment; object storage would be infrastructure
// without a demonstrated need (plan.md 22.9).
type Store struct {
	root string
}

// NewStore roots a store at dir, creating it if needed.
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("storage directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create storage directory %q: %w", dir, err)
	}
	return &Store{root: dir}, nil
}

// DocumentPDFPath is where a document's PDF lives, relative to the store root.
// Files are foldered by month so a directory never grows unbounded.
func DocumentPDFPath(documentID string, issuedAt time.Time) string {
	return filepath.Join("documents",
		issuedAt.UTC().Format("2006"), issuedAt.UTC().Format("01"),
		documentID+".pdf")
}

// BusinessLogoPath is where one uploaded logo lives. Each upload gets its own
// path so a previous logo is never overwritten: documents issued under the old
// one must keep rendering with it (plan.md 11, 14).
func BusinessLogoPath(logoID string, uploadedAt time.Time, extension string) string {
	return filepath.Join("business", "logo",
		uploadedAt.UTC().Format("2006"), logoID+extension)
}

// AttachmentPath is where an uploaded attachment lives. The name is generated
// from the attachment id, never from what the uploader called the file, so a
// crafted filename cannot steer the write (SECURITY.md 6).
func AttachmentPath(attachmentID string, uploadedAt time.Time, extension string) string {
	return filepath.Join("attachments",
		uploadedAt.UTC().Format("2006"), uploadedAt.UTC().Format("01"),
		attachmentID+extension)
}

// Blob is a file to store, with the fingerprint that will be recorded for it.
type Blob struct {
	Bytes  []byte
	SHA256 string
}

// Fingerprint computes the hash recorded alongside a stored file.
func Fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Save writes the file and returns the **canonical** path to record on the
// owning row — the location relative to the store root, with any traversal
// already resolved away. Callers persist what this returns, so a crafted input
// path can never be stored and replayed later.
// Writing is atomic — a temporary file renamed into place — so a crash cannot
// leave a half-written PDF that would fail its own hash check.
func (s *Store) Save(relativePath string, blob Blob) (string, error) {
	full, err := s.resolve(relativePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return "", fmt.Errorf("create storage directory: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename succeeds

	if _, err := temp.Write(blob.Bytes); err != nil {
		temp.Close()
		return "", fmt.Errorf("write file: %w", err)
	}
	// Flush to disk before publishing the name: a stored artifact must survive
	// a power loss.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("sync file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close file: %w", err)
	}
	if err := os.Chmod(tempName, 0o440); err != nil {
		return "", fmt.Errorf("set file permissions: %w", err)
	}
	if err := os.Rename(tempName, full); err != nil {
		return "", fmt.Errorf("publish file: %w", err)
	}

	canonical, err := s.relative(full)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

// relative expresses an absolute path inside the store as a store-relative one.
func (s *Store) relative(full string) (string, error) {
	rootAbs, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", fmt.Errorf("locate %q inside the store: %w", full, err)
	}
	return filepath.ToSlash(rel), nil
}

// Load reads a stored file and verifies it against its recorded hash, so a
// silently altered file is detected on the way out.
func (s *Store) Load(relativePath, expectedSHA256 string) ([]byte, error) {
	full, err := s.resolve(relativePath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrFileMissing, relativePath)
		}
		return nil, fmt.Errorf("read file: %w", err)
	}

	if got := Fingerprint(data); got != expectedSHA256 {
		return nil, fmt.Errorf("%w: %s recorded %s, found %s",
			ErrFileCorrupt, relativePath, expectedSHA256, got)
	}
	return data, nil
}

// resolve turns a relative path into an absolute one, refusing anything that
// would escape the store root.
func (s *Store) resolve(relativePath string) (string, error) {
	cleaned := filepath.Clean("/" + relativePath)
	full := filepath.Join(s.root, cleaned)

	rootAbs, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the storage root", relativePath)
	}
	return fullAbs, nil
}
