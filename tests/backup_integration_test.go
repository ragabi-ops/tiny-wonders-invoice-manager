package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requirePgDump skips a test that needs the PostgreSQL client tools.
func requirePgDump(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not on PATH; skipping backup tests")
	}
}

// archiveNames lists what is inside a backup archive.
func archiveNames(t *testing.T, path string) map[string]int64 {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gunzip archive: %v", err)
	}
	defer gzipReader.Close()

	names := map[string]int64{}
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		names[header.Name] = header.Size
	}
	return names
}

// archiveEntry reads one file out of an archive.
func archiveEntry(t *testing.T, path, name string) []byte {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gunzip archive: %v", err)
	}
	defer gzipReader.Close()

	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if header.Name != name {
			continue
		}
		var buffer bytes.Buffer
		if _, err := io.Copy(&buffer, reader); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return buffer.Bytes()
	}

	t.Fatalf("the archive has no entry %q", name)
	return nil
}

// newestArchive returns the newest backup in a directory.
func newestArchive(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read backup directory: %v", err)
	}

	newest := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "backup-") {
			if entry.Name() > newest {
				newest = entry.Name()
			}
		}
	}
	if newest == "" {
		t.Fatalf("no archive found in %s", dir)
	}
	return filepath.Join(dir, newest)
}

func TestBackupContainsTheDatabaseAndTheArtifacts(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)
	requirePgDump(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// Something to back up: an issued document with a PDF, and an expense with
	// an attachment.
	invoice := issuedInvoice(t, c, h.aCustomer(t, c), 25_000)

	expense := createExpense(t, c, map[string]any{
		"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 5_000,
	})
	if resp := uploadAttachment(t, c, expense.ID, "קבלה.pdf", tinyPDF); resp.status != http.StatusCreated {
		t.Fatalf("upload attachment = %d %s", resp.status, resp.body)
	}

	resp := c.post("/api/v1/backups/run", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("run backup = %d %s, want 200", resp.status, resp.body)
	}

	var run struct {
		State        string `json:"state"`
		ArchivePath  string `json:"archive_path"`
		ArchiveBytes int64  `json:"archive_bytes"`
		ArchiveSHA   string `json:"archive_sha256"`
		FileCount    int    `json:"file_count"`
	}
	resp.decode(t, &run)

	if run.State != "SUCCEEDED" {
		t.Fatalf("state = %q, want SUCCEEDED", run.State)
	}
	if run.ArchiveBytes <= 0 || run.ArchiveSHA == "" {
		t.Fatalf("run = %+v, want a sized archive with a digest", run)
	}

	names := archiveNames(t, run.ArchivePath)

	if names["database.dump"] <= 0 {
		t.Error("the archive has no database dump")
	}
	if _, ok := names["manifest.json"]; !ok {
		t.Error("the archive has no manifest")
	}

	// The stored PDF and the attachment must both be in there: they cannot be
	// recreated from the database alone.
	pdfs, attachments := 0, 0
	for name := range names {
		switch {
		case strings.HasPrefix(name, "storage/documents/"):
			pdfs++
		case strings.HasPrefix(name, "storage/attachments/"):
			attachments++
		case strings.HasPrefix(name, "storage/exports/"):
			t.Errorf("the archive carries an export (%s); exports are rebuilt on demand", name)
		}
	}
	if pdfs == 0 {
		t.Error("no issued PDF was backed up")
	}
	if attachments == 0 {
		t.Error("no expense attachment was backed up")
	}

	// The dump must really be one, not an empty file.
	dump := archiveEntry(t, run.ArchivePath, "database.dump")
	if !bytes.HasPrefix(dump, []byte("PGDMP")) {
		t.Fatalf("database.dump is not a pg_dump archive (starts %q)", firstBytes(dump, 8))
	}

	var parsed map[string]any
	if err := json.Unmarshal(archiveEntry(t, run.ArchivePath, "manifest.json"), &parsed); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if parsed["format"] != "tiny-wonders-invoice-backup/1" {
		t.Errorf("manifest format = %v", parsed["format"])
	}

	_ = invoice
}

// TestRestoreFromBackupSmokeTest is plan.md 19 item 12: prove that an archive
// can actually be restored, not merely that it was written.
//
// It restores into a scratch database and checks that the financial records
// came back intact. A backup nobody has restored is a hope, not a backup.
func TestRestoreFromBackupSmokeTest(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)
	requirePgDump(t)

	if _, err := exec.LookPath("pg_restore"); err != nil {
		t.Skip("pg_restore is not on PATH")
	}

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	customerID := h.aCustomer(t, c)
	invoice := issuedInvoice(t, c, customerID, 42_000)
	payment := recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 42_000,
		"received_at": "2026-09-08", "method": "BIT",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 42_000}},
	})

	resp := c.post("/api/v1/backups/run", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("run backup = %d %s", resp.status, resp.body)
	}
	var run struct {
		ArchivePath string `json:"archive_path"`
	}
	resp.decode(t, &run)

	// Unpack the dump.
	dump := archiveEntry(t, run.ArchivePath, "database.dump")
	dumpPath := filepath.Join(t.TempDir(), "database.dump")
	if err := os.WriteFile(dumpPath, dump, 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	// Restore into a scratch database, exactly as the runbook says.
	ctx := context.Background()
	restoreDB := "invoice_restore_check"

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if _, err := h.pool.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("database unavailable: %v", err)
	}

	// Drop and recreate through a connection to a different database.
	maintenanceURL := strings.Replace(adminURL, "/invoice_test?", "/postgres?", 1)
	for _, statement := range []string{
		`DROP DATABASE IF EXISTS ` + restoreDB,
		`CREATE DATABASE ` + restoreDB,
	} {
		command := exec.CommandContext(ctx, "psql", maintenanceURL, "-v", "ON_ERROR_STOP=1", "-c", statement)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("cannot manage a scratch database (%v): %s", err, output)
		}
	}
	t.Cleanup(func() {
		command := exec.Command("psql", maintenanceURL, "-c", `DROP DATABASE IF EXISTS `+restoreDB)
		_ = command.Run()
	})

	restoreURL := strings.Replace(adminURL, "/invoice_test?", "/"+restoreDB+"?", 1)
	restore := exec.CommandContext(ctx, "pg_restore",
		"--no-owner", "--no-privileges", "--dbname="+restoreURL, dumpPath)

	// pg_restore exits non-zero for ignorable errors too. A cross-version dump
	// (pg_dump 17 into a PostgreSQL 16 server) emits settings the server does
	// not know and reports them as ignored errors. What decides whether the
	// backup is usable is the state of the restored database, checked below —
	// but the mismatch is still worth surfacing, because it should not happen
	// in a correctly configured deployment.
	if output, err := restore.CombinedOutput(); err != nil {
		text := string(output)
		if !strings.Contains(text, "errors ignored on restore") {
			t.Fatalf("pg_restore failed: %v\n%s", err, text)
		}
		t.Logf("pg_restore reported ignorable errors, most likely a client/server "+
			"version mismatch; verifying the restored data anyway:\n%s", text)
	}

	// The restored database must hold the same financial facts.
	check := func(query string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "psql", restoreURL, "-tAc", query)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("query restored database: %v", err)
		}
		return strings.TrimSpace(string(output))
	}

	if got := check(`SELECT count(*) FROM documents WHERE state = 'ISSUED'`); got != "2" {
		t.Errorf("restored issued documents = %s, want 2 (the invoice and its receipt)", got)
	}
	if got := check(`SELECT total_agorot FROM documents WHERE id = '` + invoice.ID + `'`); got != "42000" {
		t.Errorf("restored invoice total = %s, want 42000", got)
	}
	if got := check(`SELECT amount_agorot FROM payments WHERE id = '` + payment.ID + `'`); got != "42000" {
		t.Errorf("restored payment = %s, want 42000", got)
	}
	// The snapshot is what makes a historical document renderable; it must
	// survive a restore intact.
	if got := check(`SELECT snapshot IS NOT NULL FROM documents WHERE id = '` + invoice.ID + `'`); got != "t" {
		t.Error("the restored document lost its snapshot")
	}
	// And the guarantees must still be enforced after a restore, not just the
	// data: a restored database with no triggers is a liability.
	command := exec.CommandContext(ctx, "psql", restoreURL, "-v", "ON_ERROR_STOP=1", "-c",
		`UPDATE documents SET total_agorot = 1 WHERE id = '`+invoice.ID+`'`)
	if output, err := command.CombinedOutput(); err == nil {
		t.Errorf("the restored database allowed an issued document to be edited: %s", output)
	}
}

func TestBackupStatusReportsStalenessHonestly(t *testing.T) {
	h := newHarness(t)
	requirePgDump(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	var before struct {
		Configured      bool     `json:"configured"`
		Stale           bool     `json:"stale"`
		LastSucceededAt *string  `json:"last_succeeded_at"`
		Hours           *float64 `json:"hours_since_success"`
	}
	c.get("/api/v1/backups/status").decode(t, &before)

	if !before.Configured {
		t.Fatal("the test harness did not configure a backup directory")
	}
	// Never having backed up is the stalest state there is, and must be
	// reported as such rather than as "fine, nothing to report".
	if !before.Stale || before.LastSucceededAt != nil {
		t.Fatalf("status before any backup = %+v, want stale with no last success", before)
	}

	if resp := c.post("/api/v1/backups/run", nil); resp.status != http.StatusOK {
		t.Fatalf("run backup = %d %s", resp.status, resp.body)
	}

	var after struct {
		Stale           bool     `json:"stale"`
		LastSucceededAt *string  `json:"last_succeeded_at"`
		Hours           *float64 `json:"hours_since_success"`
		LastRunState    *string  `json:"last_run_state"`
	}
	c.get("/api/v1/backups/status").decode(t, &after)

	if after.Stale {
		t.Error("status is stale immediately after a successful backup")
	}
	if after.LastSucceededAt == nil || after.Hours == nil || *after.Hours > 1 {
		t.Fatalf("status after a backup = %+v", after)
	}
	if after.LastRunState == nil || *after.LastRunState != "SUCCEEDED" {
		t.Errorf("last run state = %v, want SUCCEEDED", after.LastRunState)
	}
}

func TestBackupIsAuditedAndRecorded(t *testing.T) {
	h := newHarness(t)
	requirePgDump(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := c.post("/api/v1/backups/run", nil); resp.status != http.StatusOK {
		t.Fatalf("run backup = %d %s", resp.status, resp.body)
	}

	var history struct {
		Runs []struct {
			State   string `json:"state"`
			Trigger string `json:"trigger"`
		} `json:"runs"`
	}
	c.get("/api/v1/backups/runs").decode(t, &history)

	if len(history.Runs) != 1 || history.Runs[0].State != "SUCCEEDED" {
		t.Fatalf("runs = %+v, want one succeeded run", history.Runs)
	}
	if history.Runs[0].Trigger != "MANUAL" {
		t.Errorf("trigger = %q, want MANUAL", history.Runs[0].Trigger)
	}

	var log struct {
		Events []struct {
			Operation string `json:"operation"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?entity_type=backup").decode(t, &log)

	found := false
	for _, event := range log.Events {
		if event.Operation == "BACKUP_COMPLETED" {
			found = true
		}
	}
	if !found {
		t.Error("the backup was not audited")
	}

	// The record of what was backed up and when is evidence; it is not deleted.
	if _, err := h.pool.Exec(context.Background(), `DELETE FROM backup_runs`); err == nil {
		t.Error("backup_runs accepted a DELETE")
	}
}

func TestOnlyOwnerCanTouchBackups(t *testing.T) {
	h := newHarness(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const email = "accountant-backup@example.test"
	const password = "accountant-password-x"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "רו״ח", "password": password, "roles": []string{"ACCOUNTANT"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create accountant = %d %s", resp.status, resp.body)
	}

	accountant := h.newClient()
	accountant.loginOK(email, password)

	// The archive location and the backup machinery are the owner's business.
	for _, path := range []string{"/api/v1/backups/status", "/api/v1/backups/runs"} {
		if resp := accountant.get(path); resp.status != http.StatusForbidden {
			t.Errorf("accountant GET %s = %d, want 403", path, resp.status)
		}
	}
	if resp := accountant.post("/api/v1/backups/run", nil); resp.status != http.StatusForbidden {
		t.Errorf("accountant run backup = %d, want 403", resp.status)
	}
}

func TestBackupPrunesOldArchivesButNeverTheLastOne(t *testing.T) {
	h := newHarness(t)
	requirePgDump(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := c.post("/api/v1/backups/run", nil); resp.status != http.StatusOK {
		t.Fatalf("run backup = %d %s", resp.status, resp.body)
	}

	var status struct {
		Directory string `json:"directory"`
	}
	c.get("/api/v1/backups/status").decode(t, &status)

	archive := newestArchive(t, status.Directory)

	// Age it well past the retention window.
	old := time.Now().AddDate(0, 0, -400)
	if err := os.Chtimes(archive, old, old); err != nil {
		t.Fatalf("age the archive: %v", err)
	}

	if resp := c.post("/api/v1/backups/run", nil); resp.status != http.StatusOK {
		t.Fatalf("second backup = %d %s", resp.status, resp.body)
	}

	entries, err := os.ReadDir(status.Directory)
	if err != nil {
		t.Fatalf("read backup directory: %v", err)
	}
	archives := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "backup-") {
			archives++
		}
	}
	// The aged one is pruned; the fresh one remains. Pruning must never leave
	// the folder empty.
	if archives != 1 {
		t.Fatalf("%d archives after pruning, want 1", archives)
	}
}

// TestBackupStatusReportsToolVersions guards the check behind plan.md 18's
// "a backup is only real once restored". The version comparison is the only
// thing that catches a pg_dump whose archives restore badly while reporting
// success, and it is reached through a `SHOW server_version_num` that returns
// text. Scanning that into an int fails, which silently removed the whole
// guard: the status call dropped the tools block and the scheduler logged
// "cannot run pg_dump" no matter how healthy pg_dump actually was.
func TestBackupStatusReportsToolVersions(t *testing.T) {
	h := newHarness(t)
	requirePgDump(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	var status struct {
		Configured bool `json:"configured"`
		Tools      *struct {
			PgDumpMajor int    `json:"pg_dump_major"`
			ServerMajor int    `json:"server_major"`
			Mismatch    bool   `json:"version_mismatch"`
			Warning     string `json:"version_warning"`
		} `json:"tools"`
	}
	c.get("/api/v1/backups/status").decode(t, &status)

	if !status.Configured {
		t.Fatal("the test harness did not configure a backup directory")
	}
	if status.Tools == nil {
		t.Fatal("status reported no tool versions; the version check failed instead of comparing")
	}
	if status.Tools.ServerMajor == 0 {
		t.Error("server major version is 0; SHOW server_version_num was not read")
	}
	if status.Tools.PgDumpMajor == 0 {
		t.Error("pg_dump major version is 0; pg_dump --version was not parsed")
	}

	// The comparison must agree with itself, in both directions, so a real
	// mismatch cannot be reported as fine and a match cannot raise a false alarm.
	wantMismatch := status.Tools.PgDumpMajor != status.Tools.ServerMajor
	if status.Tools.Mismatch != wantMismatch {
		t.Errorf("version_mismatch = %v for pg_dump %d vs server %d, want %v",
			status.Tools.Mismatch, status.Tools.PgDumpMajor, status.Tools.ServerMajor, wantMismatch)
	}
	if wantMismatch && status.Tools.Warning == "" {
		t.Error("a version mismatch was reported with no warning to explain it")
	}
}
