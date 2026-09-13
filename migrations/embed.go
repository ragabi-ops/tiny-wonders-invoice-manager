// Package migrations holds the ordered, forward-only SQL migrations, embedded
// into the binary so a deployment never depends on files on disk or on a
// third-party migration CLI (ARCHITECTURE.md, Migrations).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
