// Package backup produces the daily archive of everything that cannot be
// recreated: the database, the issued PDFs, the expense attachments and the
// business logo (plan.md 18).
//
// The archive is a plain gzipped tar. It is deliberately not encrypted: the
// operator syncs the folder to storage they control, and an unencrypted archive
// has one property that matters more here than confidentiality — there is no
// key to lose. A backup that cannot be decrypted is not a backup. The trade-off
// is recorded in SECURITY.md.
//
// Legal retention and backup rotation are separate concerns. Pruning old
// archives never removes a financial record: those live in the database and the
// artifact store, which are never pruned (plan.md 18).
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Trigger says what asked for a backup.
const (
	TriggerScheduled = "SCHEDULED"
	TriggerManual    = "MANUAL"
)

// State values, mirroring the database CHECK.
const (
	StateRunning   = "RUNNING"
	StateSucceeded = "SUCCEEDED"
	StateFailed    = "FAILED"
)

// ErrNotConfigured means no backup directory is set, so backups cannot run.
var ErrNotConfigured = errors.New("no backup directory is configured")

// ErrPgDumpMissing means the database dump tool could not be run.
var ErrPgDumpMissing = errors.New("pg_dump could not be run")

// Run is one recorded backup attempt.
type Run struct {
	ID           string     `json:"id"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	State        string     `json:"state"`
	ArchivePath  *string    `json:"archive_path"`
	ArchiveBytes *int64     `json:"archive_bytes"`
	ArchiveSHA   *string    `json:"archive_sha256"`
	FileCount    *int       `json:"file_count"`
	Trigger      string     `json:"trigger"`
	Error        *string    `json:"error"`
}

// Config is what the service needs to produce an archive.
type Config struct {
	// Directory is where archives are written. The operator syncs it.
	Directory string
	// DatabaseURL is passed to pg_dump.
	DatabaseURL string
	// StorageDir holds the issued PDFs, attachments and logo.
	StorageDir string
	// PgDumpPath allows pointing at a specific binary when several are
	// installed; empty means "pg_dump on PATH".
	PgDumpPath string
	// RetentionDays bounds how many archives are kept in the directory. It has
	// nothing to do with how long financial records are kept.
	RetentionDays int
	// Location is the business timezone, used for the daily schedule.
	Location *time.Location
	// Hour and Minute are when the daily backup runs, in the business timezone.
	Hour   int
	Minute int
}

// Configured reports whether backups can run at all.
func (c Config) Configured() bool { return strings.TrimSpace(c.Directory) != "" }

// Service produces and records backups.
type Service struct {
	cfg   Config
	pool  *db.Pool
	audit *audit.Recorder
}

// NewService wires the backup service.
func NewService(cfg Config, pool *db.Pool, recorder *audit.Recorder) *Service {
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 30
	}
	return &Service{cfg: cfg, pool: pool, audit: recorder}
}

// Configured reports whether backups can run.
func (s *Service) Configured() bool { return s.cfg.Configured() }

// Directory is where archives are written.
func (s *Service) Directory() string { return s.cfg.Directory }

// manifest describes an archive to whoever opens it later, possibly without
// this software to hand.
type manifest struct {
	Format        string    `json:"format"`
	CreatedAt     time.Time `json:"created_at"`
	BusinessDate  string    `json:"business_date"`
	DatabaseDump  string    `json:"database_dump"`
	DumpFormat    string    `json:"database_dump_format"`
	StoragePrefix string    `json:"storage_prefix"`
	FileCount     int       `json:"file_count"`
	RestoreHint   string    `json:"restore_hint"`
	Note          string    `json:"note"`
}

// Create produces one archive and records the attempt either way.
//
// A failure is recorded, not swallowed: an operator who cannot see that last
// night's backup failed does not have backups (plan.md 18).
func (s *Service) Create(ctx context.Context, trigger string, actor *users.User) (*Run, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}

	runID, err := s.begin(ctx, trigger, actor)
	if err != nil {
		return nil, err
	}

	result, buildErr := s.build(ctx)
	if buildErr != nil {
		if err := s.finishFailed(ctx, runID, buildErr, actor); err != nil {
			return nil, err
		}
		return nil, buildErr
	}

	if err := s.finishSucceeded(ctx, runID, result, actor); err != nil {
		return nil, err
	}

	// Pruning is best effort: a full disk later is a smaller problem than
	// reporting a good backup as failed.
	if err := s.prune(); err != nil {
		return s.Get(ctx, runID)
	}
	return s.Get(ctx, runID)
}

// buildResult is what one archive turned out to be.
type buildResult struct {
	Path      string
	Bytes     int64
	SHA256    string
	FileCount int
}

// build writes the archive: the database dump first, then every stored file.
func (s *Service) build(ctx context.Context) (*buildResult, error) {
	if err := os.MkdirAll(s.cfg.Directory, 0o750); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}

	now := time.Now().UTC()
	name := "backup-" + now.Format("2006-01-02T150405Z") + ".tar.gz"
	finalPath := filepath.Join(s.cfg.Directory, name)

	// Write to a temporary name and rename at the end, so a crashed run never
	// leaves a truncated archive that looks complete.
	temp, err := os.CreateTemp(s.cfg.Directory, ".backup-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary archive: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		temp.Close()
		os.Remove(tempName) // no-op once renamed
	}()

	hasher := sha256.New()
	// Hash what is actually written to disk, so the recorded digest describes
	// the file an operator will later restore from.
	multi := io.MultiWriter(temp, hasher)

	gzipWriter := gzip.NewWriter(multi)
	tarWriter := tar.NewWriter(gzipWriter)

	dump, err := s.dumpDatabase(ctx)
	if err != nil {
		return nil, err
	}
	defer os.Remove(dump)

	if err := addFile(tarWriter, dump, "database.dump"); err != nil {
		return nil, fmt.Errorf("add database dump: %w", err)
	}

	fileCount, err := s.addStorage(tarWriter)
	if err != nil {
		return nil, err
	}

	body, err := json.MarshalIndent(manifest{
		Format:        "tiny-wonders-invoice-backup/1",
		CreatedAt:     now,
		BusinessDate:  time.Now().In(s.cfg.Location).Format("2006-01-02"),
		DatabaseDump:  "database.dump",
		DumpFormat:    "pg_dump custom format (-Fc); restore with pg_restore",
		StoragePrefix: "storage/",
		FileCount:     fileCount,
		RestoreHint:   "See docs/RUNBOOK.md. Restore the database first, then unpack storage/ into STORAGE_DIR.",
		Note: "Contains customer identity data and complete financial records. " +
			"Not encrypted: protect the folder it is synced to.",
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	if err := addBytes(tarWriter, "manifest.json", body, now); err != nil {
		return nil, err
	}

	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	// Flush to disk before publishing the name: a backup that only exists in
	// the page cache is not a backup.
	if err := temp.Sync(); err != nil {
		return nil, fmt.Errorf("sync archive: %w", err)
	}

	info, err := temp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}
	if err := temp.Close(); err != nil {
		return nil, fmt.Errorf("close archive: %w", err)
	}
	if err := os.Chmod(tempName, 0o440); err != nil {
		return nil, fmt.Errorf("set archive permissions: %w", err)
	}
	if err := os.Rename(tempName, finalPath); err != nil {
		return nil, fmt.Errorf("publish archive: %w", err)
	}

	return &buildResult{
		Path:      finalPath,
		Bytes:     info.Size(),
		SHA256:    hex.EncodeToString(hasher.Sum(nil)),
		FileCount: fileCount,
	}, nil
}

// dumpDatabase runs pg_dump into a temporary file and returns its path.
//
// The custom format is used because pg_restore can then rebuild into an empty
// database in one command, which is what the runbook tells an operator to do
// under pressure.
func (s *Service) dumpDatabase(ctx context.Context) (string, error) {
	binary := s.cfg.PgDumpPath
	if strings.TrimSpace(binary) == "" {
		binary = "pg_dump"
	}

	temp, err := os.CreateTemp("", "invoice-dump-*.pgdump")
	if err != nil {
		return "", fmt.Errorf("create temporary dump: %w", err)
	}
	path := temp.Name()
	temp.Close()

	// A hung dump must not hold the backup open forever.
	dumpCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	command := exec.CommandContext(dumpCtx, binary,
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--file="+path,
		s.cfg.DatabaseURL)

	var stderr strings.Builder
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		os.Remove(path)
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%w: %s", ErrPgDumpMissing, detail)
	}
	return path, nil
}

// addStorage copies the artifact store into the archive.
//
// Exports are skipped: they are rebuilt on demand from the data that is being
// backed up, so carrying them would double the archive for nothing.
func (s *Service) addStorage(tarWriter *tar.Writer) (int, error) {
	root := s.cfg.StorageDir
	if strings.TrimSpace(root) == "" {
		return 0, nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(relative), "exports/") {
			return nil
		}

		if err := addFile(tarWriter, path, filepath.ToSlash(filepath.Join("storage", relative))); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("add storage: %w", err)
	}
	return count, nil
}

func addFile(tarWriter *tar.Writer, source, name string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if err := tarWriter.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o440,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Format:  tar.FormatPAX, // UTF-8 names, for Hebrew attachment filenames
	}); err != nil {
		return err
	}

	_, err = io.Copy(tarWriter, file)
	return err
}

func addBytes(tarWriter *tar.Writer, name string, body []byte, modTime time.Time) error {
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o440,
		Size:    int64(len(body)),
		ModTime: modTime,
		Format:  tar.FormatPAX,
	}); err != nil {
		return err
	}
	_, err := tarWriter.Write(body)
	return err
}

// prune removes archives older than the retention window.
//
// This is disk hygiene, not records management: the financial records
// themselves live in the database and the artifact store and are never pruned
// (plan.md 18).
func (s *Service) prune() error {
	if s.cfg.RetentionDays <= 0 {
		return nil
	}

	entries, err := os.ReadDir(s.cfg.Directory)
	if err != nil {
		return err
	}

	cutoff := time.Now().AddDate(0, 0, -s.cfg.RetentionDays)
	var archives []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "backup-") {
			continue
		}
		archives = append(archives, entry)
	}

	// Never prune the last archive standing, however old it is. An old backup
	// is worth more than none.
	if len(archives) <= 1 {
		return nil
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Name() < archives[j].Name() })

	for _, entry := range archives[:len(archives)-1] {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.cfg.Directory, entry.Name()))
		}
	}
	return nil
}
