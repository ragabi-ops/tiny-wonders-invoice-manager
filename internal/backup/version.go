package backup

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ToolVersions describes the dump tool and the server it will dump.
type ToolVersions struct {
	PgDumpMajor int    `json:"pg_dump_major"`
	ServerMajor int    `json:"server_major"`
	Mismatch    bool   `json:"version_mismatch"`
	Warning     string `json:"version_warning,omitempty"`
}

var versionPattern = regexp.MustCompile(`(\d+)`)

// CheckToolVersions compares pg_dump's major version with the server's.
//
// This matters more than it looks. A newer pg_dump happily dumps an older
// server, but the dump it writes can contain settings the older server does not
// understand — pg_dump 17 emits `SET transaction_timeout`, which PostgreSQL 16
// rejects on restore. The backup then looks successful while its restore is
// degraded, which is the worst possible failure mode for a backup.
func (s *Service) CheckToolVersions(ctx context.Context) (*ToolVersions, error) {
	versions := &ToolVersions{}

	binary := s.cfg.PgDumpPath
	if strings.TrimSpace(binary) == "" {
		binary = "pg_dump"
	}

	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPgDumpMissing, err)
	}
	if match := versionPattern.FindString(string(output)); match != "" {
		versions.PgDumpMajor, _ = strconv.Atoi(match)
	}

	// SHOW returns text, not an integer, so this must be scanned as a string
	// and converted. Scanning straight into an int fails, and the failure is
	// silent in the worst way: the mismatch guard below never runs.
	var serverVersion string
	if err := s.pool.QueryRow(ctx, `SHOW server_version_num`).Scan(&serverVersion); err != nil {
		return nil, fmt.Errorf("read server version: %w", err)
	}
	serverNum, err := strconv.Atoi(strings.TrimSpace(serverVersion))
	if err != nil {
		return nil, fmt.Errorf("parse server version %q: %w", serverVersion, err)
	}
	versions.ServerMajor = serverNum / 10000

	if versions.PgDumpMajor != 0 && versions.ServerMajor != 0 &&
		versions.PgDumpMajor != versions.ServerMajor {
		versions.Mismatch = true
		versions.Warning = fmt.Sprintf(
			"pg_dump is version %d but the server is %d. The dump may contain settings the server "+
				"cannot restore. Install a matching client, or set PG_DUMP_PATH to one.",
			versions.PgDumpMajor, versions.ServerMajor)
	}

	return versions, nil
}
