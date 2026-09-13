// Package exports builds the accountant hand-off: one ZIP holding CSVs of
// everything, the issued PDFs, and the expense attachments (plan.md 15).
//
// Building can take a while on a full year, so it runs as a background job and
// the operator downloads the result when it is ready (plan.md 17).
package exports

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/jobs"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// utf8BOM is prepended to every CSV. Without it, Excel on Windows opens a
// Hebrew UTF-8 file as mojibake — which is exactly how the accountant will open
// it (plan.md 15).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Request describes an export to build.
type Request struct {
	From string `json:"from"`
	To   string `json:"to"`
	// IncludePDFs and IncludeAttachments let an operator ask for the CSVs alone
	// when the files are large and the accountant only wants the numbers.
	IncludePDFs        bool   `json:"include_pdfs"`
	IncludeAttachments bool   `json:"include_attachments"`
	ActorID            string `json:"actor_id"`
	ActorEmail         string `json:"actor_email"`
	RequestID          string `json:"request_id"`
}

// Result is where a finished export landed.
type Result struct {
	JobID    string `json:"job_id"`
	Filename string `json:"filename"`
	Path     string `json:"storage_path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
}

// Service builds exports.
type Service struct {
	pool  *db.Pool
	store *storage.Store
	queue *jobs.Queue
	audit *audit.Recorder
}

// NewService wires the export service.
func NewService(pool *db.Pool, store *storage.Store, queue *jobs.Queue, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, store: store, queue: queue, audit: recorder}
}

// Queue schedules an export build and returns the job that will produce it.
func (s *Service) Queue(ctx context.Context, actor *users.User, request Request) (string, error) {
	request.ActorID = actor.ID
	request.ActorEmail = actor.Email

	var jobID string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := s.queue.Enqueue(ctx, tx, jobs.EnqueueParams{
			Kind:    jobs.KindBuildExport,
			Payload: request,
			// Building is deterministic and idempotent; a couple of retries
			// cover a transient disk or database hiccup.
			MaxAttempts: 3,
			ActorID:     actor.ID,
		})
		if err != nil {
			return err
		}
		jobID = id

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpExportRequested,
			EntityType:  audit.EntityExport,
			EntityID:    id,
			RequestID:   request.RequestID,
			After:       map[string]any{"from": request.From, "to": request.To},
		})
	})
	return jobID, err
}

// HandleBuildJob builds the ZIP. It is registered with the job worker.
func (s *Service) HandleBuildJob(ctx context.Context, job *jobs.Job) (any, error) {
	var request Request
	if err := json.Unmarshal(job.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode export request: %w", err)
	}

	result, err := s.Build(ctx, job.ID, request)
	if err != nil {
		return nil, err
	}

	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: request.ActorID,
			ActorEmail:  request.ActorEmail,
			Operation:   audit.OpExportBuilt,
			EntityType:  audit.EntityExport,
			EntityID:    job.ID,
			After: map[string]any{
				"filename": result.Filename,
				"bytes":    result.Bytes,
				"sha256":   result.SHA256,
			},
		})
	}); err != nil {
		return nil, err
	}

	// Stored on the job, which is how the download endpoint finds the archive.
	return result, nil
}

// Build assembles the archive and stores it.
func (s *Service) Build(ctx context.Context, jobID string, request Request) (*Result, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)

	sections := []struct {
		name  string
		write func(context.Context, *zip.Writer, Request) error
	}{
		{"documents.csv", s.writeDocuments},
		{"payments.csv", s.writePayments},
		{"expenses.csv", s.writeExpenses},
		{"customers.csv", s.writeCustomers},
	}
	for _, section := range sections {
		if err := section.write(ctx, archive, request); err != nil {
			return nil, fmt.Errorf("build %s: %w", section.name, err)
		}
	}

	if err := s.writeManifest(ctx, archive, request); err != nil {
		return nil, err
	}

	if request.IncludePDFs {
		if err := s.writeDocumentPDFs(ctx, archive, request); err != nil {
			return nil, fmt.Errorf("copy document pdfs: %w", err)
		}
	}
	if request.IncludeAttachments {
		if err := s.writeAttachments(ctx, archive, request); err != nil {
			return nil, fmt.Errorf("copy expense attachments: %w", err)
		}
	}

	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close archive: %w", err)
	}

	data := buffer.Bytes()
	sha := storage.Fingerprint(data)
	filename := "export-" + time.Now().UTC().Format("2006-01-02") + ".zip"
	relativePath := path.Join("exports", jobID+".zip")

	if _, err := s.store.Save(relativePath, storage.Blob{Bytes: data, SHA256: sha}); err != nil {
		return nil, err
	}

	return &Result{
		JobID:    jobID,
		Filename: filename,
		Path:     relativePath,
		SHA256:   sha,
		Bytes:    int64(len(data)),
	}, nil
}

// Load reads a built export back, verified against its hash.
func (s *Service) Load(ctx context.Context, jobID, sha256 string) ([]byte, error) {
	return s.store.Load(path.Join("exports", jobID+".zip"), sha256)
}

// ------------------------------------------------------------------ CSVs --

// newCSV starts a UTF-8 CSV inside the archive, with the BOM Excel needs.
func newCSV(archive *zip.Writer, name string, header []string) (*csv.Writer, error) {
	file, err := archive.Create(name)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(utf8BOM); err != nil {
		return nil, err
	}

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	return writer, nil
}

func (s *Service) writeDocuments(ctx context.Context, archive *zip.Writer, request Request) error {
	writer, err := newCSV(archive, "documents.csv", []string{
		"מספר מסמך", "סוג", "מצב", "תאריך", "לקוח", "מספר עוסק/ת״ז לקוח",
		"סכום ביניים", "מע״מ", "סה״כ", "שולם", "יתרה", "פעילות",
		"מועד הפקה", "מצב בדיקה", "חתימת PDF",
	})
	if err != nil {
		return err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(d.series || '-' || lpad(d.number::text, 6, '0'), ''),
		       d.document_type, d.state, d.document_date,
		       c.display_name, c.business_or_id_number,
		       d.subtotal_agorot, d.vat_agorot, d.total_agorot,
		       coalesce(sum(pa.amount_agorot), 0),
		       coalesce(a.name, ''), d.issued_at, d.test_mode,
		       coalesce(d.pdf_sha256, '')
		FROM documents d
		JOIN customers c ON c.id = d.customer_id
		LEFT JOIN activities a ON a.id = d.activity_id
		LEFT JOIN payment_allocations pa ON pa.document_id = d.id
		WHERE d.state <> 'DRAFT'
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		GROUP BY d.id, c.display_name, c.business_or_id_number, a.name
		ORDER BY d.document_date, d.number`,
		nullableString(request.From), nullableString(request.To))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			fullNumber, documentType, state, customer, customerNumber string
			activity, pdfSHA                                          string
			documentDate                                              time.Time
			issuedAt                                                  *time.Time
			subtotal, vat, total, paid                                money.Amount
			testMode                                                  bool
		)
		if err := rows.Scan(&fullNumber, &documentType, &state, &documentDate,
			&customer, &customerNumber, &subtotal, &vat, &total, &paid,
			&activity, &issuedAt, &testMode, &pdfSHA); err != nil {
			return err
		}

		issued := ""
		if issuedAt != nil {
			issued = issuedAt.Format(time.RFC3339)
		}

		if err := writer.Write([]string{
			fullNumber, documentType, state, documentDate.Format("2006-01-02"),
			customer, customerNumber,
			subtotal.String(), vat.String(), total.String(), paid.String(),
			(total - paid).String(), activity, issued, boolText(testMode), pdfSHA,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	writer.Flush()
	return writer.Error()
}

func (s *Service) writePayments(ctx context.Context, archive *zip.Writer, request Request) error {
	writer, err := newCSV(archive, "payments.csv", []string{
		"תאריך קבלה", "לקוח", "סכום", "אמצעי תשלום", "אסמכתא",
		"מספר קבלה", "משויך", "על החשבון", "פעילות", "הערות",
	})
	if err != nil {
		return err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT p.received_at, c.display_name, p.amount_agorot, p.method, p.reference,
		       coalesce(r.series || '-' || lpad(r.number::text, 6, '0'), ''),
		       coalesce(sum(pa.amount_agorot), 0),
		       coalesce(a.name, ''), p.notes
		FROM payments p
		JOIN customers c ON c.id = p.customer_id
		LEFT JOIN documents r ON r.id = p.receipt_document_id
		LEFT JOIN activities a ON a.id = p.activity_id
		LEFT JOIN payment_allocations pa ON pa.payment_id = p.id
		WHERE ($1::date IS NULL OR p.received_at >= $1)
		  AND ($2::date IS NULL OR p.received_at <= $2)
		GROUP BY p.id, c.display_name, r.series, r.number, a.name
		ORDER BY p.received_at, p.created_at`,
		nullableString(request.From), nullableString(request.To))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			receivedAt                     time.Time
			customer, method, reference    string
			receiptNumber, activity, notes string
			amount, allocated              money.Amount
		)
		if err := rows.Scan(&receivedAt, &customer, &amount, &method, &reference,
			&receiptNumber, &allocated, &activity, &notes); err != nil {
			return err
		}

		if err := writer.Write([]string{
			receivedAt.Format("2006-01-02"), customer, amount.String(), method, reference,
			receiptNumber, allocated.String(), (amount - allocated).String(), activity, notes,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	writer.Flush()
	return writer.Error()
}

func (s *Service) writeExpenses(ctx context.Context, archive *zip.Writer, request Request) error {
	writer, err := newCSV(archive, "expenses.csv", []string{
		"תאריך", "ספק", "סכום", "קטגוריה", "אמצעי תשלום",
		"אסמכתא", "פעילות", "מספר קבצים מצורפים", "הערות",
	})
	if err != nil {
		return err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT e.expense_date, e.supplier, e.amount_agorot, e.category,
		       e.payment_method, e.reference, coalesce(a.name, ''),
		       (SELECT count(*) FROM attachments at
		        WHERE at.entity_type = 'expense' AND at.entity_id = e.id),
		       e.notes
		FROM expenses e
		LEFT JOIN activities a ON a.id = e.activity_id
		WHERE ($1::date IS NULL OR e.expense_date >= $1)
		  AND ($2::date IS NULL OR e.expense_date <= $2)
		ORDER BY e.expense_date, e.created_at`,
		nullableString(request.From), nullableString(request.To))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			expenseDate                           time.Time
			supplier, category, method, reference string
			activity, notes                       string
			amount                                money.Amount
			attachmentCount                       int
		)
		if err := rows.Scan(&expenseDate, &supplier, &amount, &category,
			&method, &reference, &activity, &attachmentCount, &notes); err != nil {
			return err
		}

		if err := writer.Write([]string{
			expenseDate.Format("2006-01-02"), supplier, amount.String(), category,
			method, reference, activity, fmt.Sprint(attachmentCount), notes,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	writer.Flush()
	return writer.Error()
}

func (s *Service) writeCustomers(ctx context.Context, archive *zip.Writer, _ Request) error {
	writer, err := newCSV(archive, "customers.csv", []string{
		"שם", "סוג", "שם רשמי", "מספר עוסק/ת״ז", "טלפון", "דוא״ל", "כתובת", "פעיל",
	})
	if err != nil {
		return err
	}

	// Customers are exported in full, not filtered by date: the accountant needs
	// the party behind every document in the range, whenever it was created.
	rows, err := s.pool.Query(ctx, `
		SELECT display_name, customer_type, legal_name, business_or_id_number,
		       phone, email, address, active
		FROM customers ORDER BY display_name`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, customerType, legalName, number, phone, email, address string
		var active bool
		if err := rows.Scan(&name, &customerType, &legalName, &number,
			&phone, &email, &address, &active); err != nil {
			return err
		}
		if err := writer.Write([]string{
			name, customerType, legalName, number, phone, email, address, boolText(active),
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	writer.Flush()
	return writer.Error()
}

// writeManifest records what this archive is and how it was produced, so a
// reader a year from now knows what they are holding.
func (s *Service) writeManifest(ctx context.Context, archive *zip.Writer, request Request) error {
	file, err := archive.Create("manifest.json")
	if err != nil {
		return err
	}

	var businessMode string
	_ = s.pool.QueryRow(ctx, `
		SELECT value #>> '{}' FROM regulatory_config
		WHERE key = 'business_mode' ORDER BY effective_from DESC LIMIT 1`).Scan(&businessMode)

	manifest := map[string]any{
		"generated_at":         time.Now().UTC().Format(time.RFC3339),
		"range_from":           request.From,
		"range_to":             request.To,
		"business_mode":        businessMode,
		"includes_pdfs":        request.IncludePDFs,
		"includes_attachments": request.IncludeAttachments,
		"csv_encoding":         "UTF-8 with BOM",
		"money_format":         "decimal shekels, two places",
		"note": "Documents in the TEST series were issued before the compliance " +
			"gate was cleared and are not legal documents.",
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(manifest)
}

// ---------------------------------------------------------------- files --

func (s *Service) writeDocumentPDFs(ctx context.Context, archive *zip.Writer, request Request) error {
	rows, err := s.pool.Query(ctx, `
		SELECT d.document_type, d.series || '-' || lpad(d.number::text, 6, '0'),
		       d.pdf_path, d.pdf_sha256
		FROM documents d
		WHERE d.state <> 'DRAFT' AND d.pdf_path IS NOT NULL
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		ORDER BY d.document_date, d.number`,
		nullableString(request.From), nullableString(request.To))
	if err != nil {
		return err
	}
	defer rows.Close()

	type artifact struct{ documentType, name, path, sha string }
	var artifacts []artifact

	for rows.Next() {
		var a artifact
		if err := rows.Scan(&a.documentType, &a.name, &a.path, &a.sha); err != nil {
			return err
		}
		artifacts = append(artifacts, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, a := range artifacts {
		// Load verifies the hash, so a corrupted artifact stops the export
		// rather than quietly shipping a bad file to the accountant.
		data, err := s.store.Load(a.path, a.sha)
		if err != nil {
			return err
		}
		// Foldered by type: each document type has its own counter, so an
		// invoice and a receipt can both be number 1 and would otherwise
		// collide on one name inside the archive.
		file, err := archive.Create(path.Join("pdf", a.documentType, a.name+".pdf"))
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) writeAttachments(ctx context.Context, archive *zip.Writer, request Request) error {
	rows, err := s.pool.Query(ctx, `
		SELECT at.id::text, at.original_filename, at.storage_path, at.sha256,
		       e.expense_date, e.supplier
		FROM attachments at
		JOIN expenses e ON e.id = at.entity_id AND at.entity_type = 'expense'
		WHERE ($1::date IS NULL OR e.expense_date >= $1)
		  AND ($2::date IS NULL OR e.expense_date <= $2)
		ORDER BY e.expense_date`,
		nullableString(request.From), nullableString(request.To))
	if err != nil {
		return err
	}
	defer rows.Close()

	type attachment struct {
		id, filename, path, sha, supplier string
		date                              time.Time
	}
	var attachments []attachment

	for rows.Next() {
		var a attachment
		if err := rows.Scan(&a.id, &a.filename, &a.path, &a.sha, &a.date, &a.supplier); err != nil {
			return err
		}
		attachments = append(attachments, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, a := range attachments {
		data, err := s.store.Load(a.path, a.sha)
		if err != nil {
			return err
		}
		// Prefixed with the date and supplier so the accountant can match a file
		// to its row without opening it; the id keeps names unique.
		name := fmt.Sprintf("%s_%s_%s_%s",
			a.date.Format("2006-01-02"), safeName(a.supplier), a.id[:8], safeName(a.filename))

		file, err := archive.Create(path.Join("expense-attachments", name))
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// safeName strips separators so a value cannot introduce a path inside the zip.
func safeName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\x00", "").Replace(value)
	if len([]rune(value)) > 60 {
		value = string([]rune(value)[:60])
	}
	if value == "" {
		return "file"
	}
	return value
}

func boolText(value bool) string {
	if value {
		return "כן"
	}
	return "לא"
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
