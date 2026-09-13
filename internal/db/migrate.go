package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// migrationLockID is an arbitrary but fixed key for the PostgreSQL advisory
// lock that serializes migration runs across API instances.
const migrationLockID int64 = 8_274_113_905_551_001

// Migration is one ordered SQL file.
type Migration struct {
	Version  int
	Name     string
	Checksum string
	SQL      string
}

// AppliedMigration is a row of schema_migrations.
type AppliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

// LoadMigrations parses and orders migrations from an fs.FS. File names must be
// NNNN_name.sql; the numeric prefix is the version.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, name := range entries {
		base := path.Base(name)
		prefix, rest, ok := strings.Cut(strings.TrimSuffix(base, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: expected NNNN_name.sql", base)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: version prefix is not a number: %w", base, err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migration version %d used twice: %q and %q", version, other, base)
		}
		seen[version] = base

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", base, err)
		}
		sum := sha256.Sum256(content)

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     rest,
			Checksum: hex.EncodeToString(sum[:]),
			SQL:      string(content),
		})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Migrate applies every pending migration in order. Each migration runs in its
// own transaction together with its schema_migrations row, so a failure leaves
// no partially recorded version. There are no down-migrations: a financial
// database is repaired forward (ARCHITECTURE.md, Migrations).
func Migrate(ctx context.Context, pool *Pool, fsys fs.FS, log *slog.Logger) error {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// Serialize concurrent starts. The lock is released when the connection is
	// returned to the pool.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID); err != nil {
			log.Error("release migration lock", "error", err)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    integer     PRIMARY KEY,
			name       text        NOT NULL,
			checksum   text        NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn.Conn())
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if prev, ok := applied[m.Version]; ok {
			// An edited applied migration means the database and the source tree
			// disagree about history. Refuse rather than guess.
			if prev.Checksum != m.Checksum {
				return fmt.Errorf(
					"migration %04d_%s was modified after being applied (recorded %s, found %s): add a new migration instead",
					m.Version, m.Name, prev.Checksum[:12], m.Checksum[:12])
			}
			continue
		}

		log.Info("applying migration", "version", m.Version, "name", m.Name)
		if err := applyMigration(ctx, conn.Conn(), m); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		m.Version, m.Name, m.Checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appliedMigrations(ctx context.Context, conn *pgx.Conn) (map[int]AppliedMigration, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]AppliedMigration)
	for rows.Next() {
		var a AppliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum); err != nil {
			return nil, err
		}
		applied[a.Version] = a
	}
	return applied, rows.Err()
}

// MigrationStatus reports each known migration and whether it is applied, for
// `make migrate-status` and the readiness probe.
func MigrationStatus(ctx context.Context, pool *Pool, fsys fs.FS) ([]Migration, map[int]AppliedMigration, error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return nil, nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Release()

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, nil, err
	}
	if !exists {
		return migrations, map[int]AppliedMigration{}, nil
	}

	applied, err := appliedMigrations(ctx, conn.Conn())
	if err != nil {
		return nil, nil, err
	}
	return migrations, applied, nil
}
