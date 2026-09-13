package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/compliance"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/pdf"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
	tmpl "github.com/ragabix/tiny-wonders-invoice-manager/templates"
)

// snapshotSchemaVersion versions the snapshot shape itself, so a reader years
// from now knows how to interpret an old row.
const snapshotSchemaVersion = 1

// ErrComplianceBlocked means the compliance adapter refused the action.
type ErrComplianceBlocked struct{ Reason string }

func (e *ErrComplianceBlocked) Error() string { return e.Reason }

// Service performs document use cases.
type Service struct {
	pool       *db.Pool
	repo       *Repository
	sequences  *SequenceAllocator
	audit      *audit.Recorder
	settings   *settings.Service
	compliance *compliance.Service
	renderer   pdf.Renderer
	store      *storage.Store
	location   *time.Location
}

// NewService wires the document service.
func NewService(
	pool *db.Pool,
	repo *Repository,
	sequences *SequenceAllocator,
	recorder *audit.Recorder,
	settingsService *settings.Service,
	complianceService *compliance.Service,
	renderer pdf.Renderer,
	store *storage.Store,
	location *time.Location,
) *Service {
	return &Service{
		pool: pool, repo: repo, sequences: sequences, audit: recorder,
		settings: settingsService, compliance: complianceService,
		renderer: renderer, store: store, location: location,
	}
}

// List returns a page of documents.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one document with its lines.
func (s *Service) Get(ctx context.Context, id string) (*Document, error) {
	return s.repo.GetWithLines(ctx, s.pool, id)
}

// Snapshot returns the stored snapshot of an issued document.
func (s *Service) Snapshot(ctx context.Context, id string) (json.RawMessage, error) {
	return s.repo.SnapshotJSON(ctx, s.pool, id)
}

// SequenceStates returns the numbering report.
func (s *Service) SequenceStates(ctx context.Context) ([]SequenceState, error) {
	return s.sequences.States(ctx, s.pool)
}

// ---------------------------------------------------------------- drafts --

// CreateDraft stores a new draft with server-computed totals.
func (s *Service) CreateDraft(ctx context.Context, actor *users.User, in DraftInput, requestID string) (*Document, error) {
	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, err
	}
	if decision := rules.CanIssueDocumentType(string(in.DocumentType)); !decision.Allowed {
		return nil, &ErrComplianceBlocked{Reason: decision.Reason}
	}

	totals, err := ComputeTotals(in.Lines, rules.VATApplies(), 0)
	if err != nil {
		return nil, err
	}

	var created *Document
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.assertCustomerUsable(ctx, tx, in.CustomerID); err != nil {
			return err
		}

		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO documents
				(document_type, state, customer_id, activity_id, document_date, due_date,
				 subtotal_agorot, vat_agorot, total_agorot, notes, created_by, updated_by)
			VALUES ($1, 'DRAFT', $2, $3, $4::date, $5::date, $6, $7, $8, $9, $10, $10)
			RETURNING id`,
			string(in.DocumentType), in.CustomerID, nullableString(derefOrEmpty(in.ActivityID)),
			in.DocumentDate, nullableString(derefOrEmpty(in.DueDate)),
			totals.Subtotal.Agorot(), totals.VAT.Agorot(), totals.Total.Agorot(),
			in.Notes, actor.ID).Scan(&id); err != nil {
			return err
		}

		if err := s.insertLines(ctx, tx, id, totals.Lines); err != nil {
			return err
		}

		if created, err = s.repo.GetWithLines(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDocumentDraftCreated,
			EntityType:  audit.EntityDocument,
			EntityID:    id,
			RequestID:   requestID,
			After:       map[string]any{"type": in.DocumentType, "total_agorot": totals.Total.Agorot()},
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateDraft replaces a draft's contents. Issued documents are refused here
// and again by the database trigger.
func (s *Service) UpdateDraft(ctx context.Context, actor *users.User, id string, in DraftInput, requestID string) (*Document, error) {
	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, err
	}
	if decision := rules.CanIssueDocumentType(string(in.DocumentType)); !decision.Allowed {
		return nil, &ErrComplianceBlocked{Reason: decision.Reason}
	}

	totals, err := ComputeTotals(in.Lines, rules.VATApplies(), 0)
	if err != nil {
		return nil, err
	}

	var updated *Document
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		existing, err := s.repo.GetForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if existing.State != StateDraft {
			return ErrNotDraft
		}
		if err := s.assertCustomerUsable(ctx, tx, in.CustomerID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE documents
			SET document_type = $2, customer_id = $3, activity_id = $4,
			    document_date = $5::date, due_date = $6::date,
			    subtotal_agorot = $7, vat_agorot = $8, total_agorot = $9,
			    notes = $10, updated_by = $11
			WHERE id = $1`,
			id, string(in.DocumentType), in.CustomerID, nullableString(derefOrEmpty(in.ActivityID)),
			in.DocumentDate, nullableString(derefOrEmpty(in.DueDate)),
			totals.Subtotal.Agorot(), totals.VAT.Agorot(), totals.Total.Agorot(),
			in.Notes, actor.ID); err != nil {
			return err
		}

		// Lines are replaced wholesale: a draft's lines have no identity worth
		// preserving, and this keeps line_number contiguous.
		if _, err := tx.Exec(ctx, `DELETE FROM document_lines WHERE document_id = $1`, id); err != nil {
			return err
		}
		if err := s.insertLines(ctx, tx, id, totals.Lines); err != nil {
			return err
		}

		if updated, err = s.repo.GetWithLines(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDocumentDraftUpdated,
			EntityType:  audit.EntityDocument,
			EntityID:    id,
			RequestID:   requestID,
			Before:      map[string]any{"total_agorot": existing.Total.Agorot()},
			After:       map[string]any{"total_agorot": totals.Total.Agorot()},
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteDraft removes a draft. Only drafts can be deleted, in the service and
// in the database.
func (s *Service) DeleteDraft(ctx context.Context, actor *users.User, id, requestID string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		existing, err := s.repo.GetForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if existing.State != StateDraft {
			return ErrNotDraft
		}

		if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDocumentDraftDeleted,
			EntityType:  audit.EntityDocument,
			EntityID:    id,
			RequestID:   requestID,
			Before:      map[string]any{"type": existing.DocumentType, "total_agorot": existing.Total.Agorot()},
		})
	})
}

// -------------------------------------------------------------- issuance --

// IssueResult is what issuance produced.
type IssueResult struct {
	Document *Document
	Replayed bool
}

// Issue turns a draft into an official document, atomically (plan.md 9):
//
//  1. validate the current state
//  2. allocate the official number
//  3. recompute and validate totals server-side
//  4. snapshot business, customer and document data
//  5. set the server issue timestamp
//  6. generate the final PDF
//  7. store the PDF, its hash and the template version
//  8. write the audit event
//  9. commit
//
// Everything happens in one transaction. If the PDF cannot be produced, nothing
// commits and the allocated number is returned by the rollback — a document is
// never issued without its artifact.
func (s *Service) Issue(ctx context.Context, actor *users.User, id, requestID string) (*Document, error) {
	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, err
	}

	// While the compliance gate is closed the system still issues, but into a
	// separate series and with a watermark, so nothing here can ever be
	// mistaken for — or consume the number of — a real document (plan.md 2).
	testMode := !rules.CanIssueForReal().Allowed
	series := SeriesOfficial
	if testMode {
		series = SeriesTest
	}

	businessProfile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("read business profile: %w", err)
	}

	var issued *Document
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// 1. Validate state, holding the row so a concurrent issue of the same
		// draft waits here rather than issuing it twice.
		document, err := s.repo.GetForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		switch document.State {
		case StateIssued:
			return ErrAlreadyIssued
		case StateCancelled:
			return ErrAlreadyVoid
		}

		if decision := rules.CanIssueDocumentType(string(document.DocumentType)); !decision.Allowed {
			return &ErrComplianceBlocked{Reason: decision.Reason}
		}

		lines, err := s.repo.Lines(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return ErrNoLines
		}

		// 3. Recompute the totals from the stored lines. Whatever the draft row
		// says is not trusted at this point either.
		inputs := make([]LineInput, 0, len(lines))
		for _, line := range lines {
			inputs = append(inputs, LineInput{
				ServiceID:     line.ServiceID,
				Description:   line.Description,
				Unit:          line.Unit,
				QuantityMilli: line.QuantityMilli,
				UnitPrice:     line.UnitPrice,
			})
		}
		totals, err := ComputeTotals(inputs, rules.VATApplies(), 0)
		if err != nil {
			return err
		}

		// 2. Allocate the number inside this transaction (plan.md 10).
		number, err := s.sequences.Allocate(ctx, tx, document.DocumentType, series)
		if err != nil {
			return err
		}

		// Steps 4 through 7 are shared with receipt issuance, so the two paths
		// cannot drift apart.
		finalized, err := s.finalize(ctx, tx, finalizeParams{
			Document: document,
			Lines:    lines,
			Totals:   totals,
			Business: businessProfile,
			Series:   series,
			Number:   number,
			TestMode: testMode,
			Rules:    rules,
			Actor:    actor,
			Payment:  nil,
		})
		if err != nil {
			return err
		}

		if issued, err = s.repo.GetWithLines(ctx, tx, id); err != nil {
			return err
		}

		// 8. Audit, in the same transaction as the act it records.
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDocumentIssued,
			EntityType:  audit.EntityDocument,
			EntityID:    id,
			RequestID:   requestID,
			After: map[string]any{
				"type":         document.DocumentType,
				"full_number":  FormatNumber(series, number),
				"total_agorot": totals.Total.Agorot(),
				"pdf_sha256":   finalized.SHA256,
				"test_mode":    testMode,
			},
			Reason: complianceNote(testMode),
		})
	})
	if err != nil {
		return nil, err
	}
	return issued, nil
}

// logoFor loads the logo a snapshot points at, or nil when there is none.
func (s *Service) logoFor(snapshot *Snapshot) *LogoImage {
	if snapshot.Business.LogoPath == "" || snapshot.Business.LogoSHA256 == "" {
		return nil
	}

	data, err := s.settings.LogoAt(snapshot.Business.LogoPath, snapshot.Business.LogoSHA256)
	if err != nil {
		return nil
	}
	return &LogoImage{Bytes: data}
}

func complianceNote(testMode bool) string {
	if testMode {
		return "issued in test mode: the compliance gate is not cleared"
	}
	return ""
}

// Cancel voids an issued document. The original number, snapshot and PDF are
// preserved; only the cancellation fields are added, and the database refuses
// anything else (plan.md 9).
func (s *Service) Cancel(ctx context.Context, actor *users.User, id, reason, requestID string) (*Document, error) {
	var cancelled *Document

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		document, err := s.repo.GetForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		switch document.State {
		case StateDraft:
			return ErrNotIssued
		case StateCancelled:
			return ErrAlreadyVoid
		}

		if _, err := tx.Exec(ctx, `
			UPDATE documents
			SET state = 'CANCELLED', cancelled_at = now(), cancelled_by = $2,
			    cancellation_reason = $3, updated_by = $2
			WHERE id = $1`, id, actor.ID, reason); err != nil {
			return err
		}

		// The corrective relationship is recorded even when it points at
		// itself-as-cancelled, so the trail is explicit rather than implied by
		// a state column (plan.md 9).
		if cancelled, err = s.repo.GetWithLines(ctx, tx, id); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDocumentCancelled,
			EntityType:  audit.EntityDocument,
			EntityID:    id,
			RequestID:   requestID,
			Reason:      reason,
			Before:      map[string]any{"state": string(StateIssued)},
			After:       map[string]any{"state": string(StateCancelled), "full_number": document.FullNumber},
		})
	})
	if err != nil {
		return nil, err
	}
	return cancelled, nil
}

// PDF returns the stored artifact of an issued document, verified against its
// recorded hash.
func (s *Service) PDF(ctx context.Context, id string) ([]byte, string, error) {
	ref, err := s.repo.Artifact(ctx, s.pool, id)
	if err != nil {
		return nil, "", err
	}

	data, err := s.store.Load(ref.Path, ref.SHA256)
	if err != nil {
		return nil, "", err
	}

	filename := ref.FullNumber + ".pdf"
	return data, filename, nil
}

// ------------------------------------------------------------- internals --

func (s *Service) insertLines(ctx context.Context, tx pgx.Tx, documentID string, lines []Line) error {
	for _, line := range lines {
		var serviceArg any
		if line.ServiceID != nil && *line.ServiceID != "" {
			serviceArg = *line.ServiceID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_lines
				(document_id, line_number, service_id, description, unit,
				 quantity_milli, unit_price_agorot, line_total_agorot)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			documentID, line.LineNumber, serviceArg, line.Description, line.Unit,
			line.QuantityMilli, line.UnitPrice.Agorot(), line.LineTotal.Agorot()); err != nil {
			return err
		}
	}
	return nil
}

// assertCustomerUsable refuses to build a document for an archived customer:
// archiving is how staff say "do not use this record any more".
func (s *Service) assertCustomerUsable(ctx context.Context, q db.Querier, customerID string) error {
	var active bool
	if err := q.QueryRow(ctx, `SELECT active FROM customers WHERE id = $1`, customerID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("customer %s: %w", customerID, ErrNotFound)
		}
		return err
	}
	if !active {
		return ErrCustomerHidden
	}
	return nil
}

// buildSnapshot captures the exact values used at issuance.
func (s *Service) buildSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	document *Document,
	lines []Line,
	totals *Totals,
	business settings.BusinessProfile,
	series string,
	number int64,
	issuedAt time.Time,
	requiredWording string,
	testMode bool,
	businessMode string,
	actor *users.User,
) (*Snapshot, error) {
	snapshot := &Snapshot{SchemaVersion: snapshotSchemaVersion}

	snapshot.Business.LegalName = business.LegalName
	snapshot.Business.DisplayName = business.DisplayName
	snapshot.Business.BusinessNumber = business.BusinessNumber
	snapshot.Business.Address = business.Address
	snapshot.Business.Phone = business.Phone
	snapshot.Business.Email = business.Email
	snapshot.Business.Website = business.Website
	snapshot.Business.BankDetails = business.BankDetails
	snapshot.Business.FooterNote = business.FooterNote
	snapshot.Business.LogoPath = business.LogoPath
	snapshot.Business.LogoSHA256 = business.LogoSHA256

	if err := tx.QueryRow(ctx, `
		SELECT id::text, customer_type, display_name, legal_name,
		       business_or_id_number, phone, email, address
		FROM customers WHERE id = $1`, document.CustomerID).
		Scan(&snapshot.Customer.ID, &snapshot.Customer.CustomerType,
			&snapshot.Customer.DisplayName, &snapshot.Customer.LegalName,
			&snapshot.Customer.BusinessOrIDNumber, &snapshot.Customer.Phone,
			&snapshot.Customer.Email, &snapshot.Customer.Address); err != nil {
		return nil, fmt.Errorf("snapshot customer: %w", err)
	}

	if document.ActivityID != nil {
		var activity struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := tx.QueryRow(ctx, `SELECT id::text, name FROM activities WHERE id = $1`,
			*document.ActivityID).Scan(&activity.ID, &activity.Name); err != nil {
			return nil, fmt.Errorf("snapshot activity: %w", err)
		}
		snapshot.Activity = &activity
	}

	snapshot.Document.Type = string(document.DocumentType)
	snapshot.Document.TypeHebrew = document.DocumentType.HebrewName()
	snapshot.Document.Series = series
	snapshot.Document.Number = number
	snapshot.Document.FullNumber = FormatNumber(series, number)
	snapshot.Document.DocumentDate = document.DocumentDate
	if document.DueDate != nil {
		snapshot.Document.DueDate = *document.DueDate
	}
	snapshot.Document.Currency = money.Currency
	snapshot.Document.Notes = document.Notes
	snapshot.Document.RequiredWording = requiredWording
	snapshot.Document.TestMode = testMode

	for _, line := range totals.Lines {
		snapshot.Lines = append(snapshot.Lines, struct {
			LineNumber    int    `json:"line_number"`
			Description   string `json:"description"`
			Unit          string `json:"unit"`
			QuantityMilli int64  `json:"quantity_milli"`
			UnitPrice     int64  `json:"unit_price_agorot"`
			LineTotal     int64  `json:"line_total_agorot"`
		}{
			LineNumber:    line.LineNumber,
			Description:   line.Description,
			Unit:          line.Unit,
			QuantityMilli: line.QuantityMilli,
			UnitPrice:     line.UnitPrice.Agorot(),
			LineTotal:     line.LineTotal.Agorot(),
		})
	}

	snapshot.Totals.Subtotal = totals.Subtotal.Agorot()
	snapshot.Totals.VAT = totals.VAT.Agorot()
	snapshot.Totals.Total = totals.Total.Agorot()

	snapshot.IssuedAt = issuedAt
	snapshot.IssuedByEmail = actor.Email
	snapshot.TemplateVersion = tmpl.CurrentDocumentVersion
	snapshot.BusinessMode = businessMode

	return snapshot, nil
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ------------------------------------------------- shared issuance tail ----

// finalizeParams carries everything the shared part of issuance needs.
type finalizeParams struct {
	Document *Document
	Lines    []Line
	Totals   *Totals
	Business settings.BusinessProfile
	Series   string
	Number   int64
	TestMode bool
	Rules    *compliance.Rules
	Actor    *users.User
	// Payment is set only when the document being issued is a receipt.
	Payment *PaymentSnapshot
}

// finalized is what the shared tail produced.
type finalized struct {
	SHA256     string
	IssuedAt   time.Time
	FullNumber string
}

// finalize performs steps 4 to 7 of plan.md 9 — snapshot, server issue time,
// render, store, and the state change to ISSUED — for both the draft flow and
// the receipt flow.
//
// It must be called inside the issuance transaction, after the number has been
// allocated. A renderer failure returns an error, which rolls the whole thing
// back: no artifact, no document, no consumed number.
func (s *Service) finalize(ctx context.Context, tx pgx.Tx, p finalizeParams) (*finalized, error) {
	// The issue time is the server's, never the client's (plan.md 3.2).
	issuedAt := time.Now().UTC()

	requiredWording := ""
	if p.Rules.RequiresExemptDealerWording() {
		requiredWording = "עוסק פטור"
	}

	snapshot, err := s.buildSnapshot(ctx, tx, p.Document, p.Lines, p.Totals,
		p.Business, p.Series, p.Number, issuedAt, requiredWording,
		p.TestMode, string(p.Rules.Mode), p.Actor)
	if err != nil {
		return nil, err
	}
	snapshot.Payment = p.Payment

	// The renderer has no network access, so the logo is inlined as a data URI.
	// A logo that cannot be read is not worth failing an issuance over: the
	// document is still complete and correct without its mark.
	logo := s.logoFor(snapshot)

	html, err := RenderHTML(snapshot, s.location, RenderOptions{Logo: logo})
	if err != nil {
		return nil, fmt.Errorf("render document html: %w", err)
	}
	rendered, err := s.renderer.Render(ctx, html)
	if err != nil {
		return nil, err
	}

	// Store the artifact before the row that points at it, so a committed
	// document always has a readable PDF.
	relativePath := storage.DocumentPDFPath(p.Document.ID, issuedAt)
	if _, err := s.store.Save(relativePath, storage.Blob{Bytes: rendered.Bytes, SHA256: rendered.SHA256}); err != nil {
		return nil, err
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE documents
		SET state = 'ISSUED', series = $2, number = $3,
		    subtotal_agorot = $4, vat_agorot = $5, total_agorot = $6,
		    required_wording = $7, issued_at = $8, issued_by = $9,
		    snapshot = $10, pdf_path = $11, pdf_sha256 = $12, pdf_bytes = $13,
		    template_version = $14, test_mode = $15, updated_by = $9
		WHERE id = $1`,
		p.Document.ID, p.Series, p.Number,
		p.Totals.Subtotal.Agorot(), p.Totals.VAT.Agorot(), p.Totals.Total.Agorot(),
		requiredWording, issuedAt, p.Actor.ID,
		snapshotJSON, relativePath, rendered.SHA256, rendered.ByteSize,
		tmpl.CurrentDocumentVersion, p.TestMode); err != nil {
		return nil, err
	}

	return &finalized{
		SHA256:     rendered.SHA256,
		IssuedAt:   issuedAt,
		FullNumber: FormatNumber(p.Series, p.Number),
	}, nil
}

// ------------------------------------------------------ receipt issuance ----

// ReceiptRequest describes the receipt to issue for a payment.
type ReceiptRequest struct {
	CustomerID   string
	ActivityID   *string
	DocumentDate string
	Notes        string
	Lines        []LineInput
	Payment      *PaymentSnapshot
	// SettledDocumentIDs get a RECEIPT_FOR edge back from the receipt, so the
	// relationship is recorded explicitly rather than inferred (plan.md 9).
	SettledDocumentIDs []string
}

// IssueReceipt creates and issues a receipt inside the caller's transaction.
//
// It is deliberately not a standalone command: a payment and its receipt must
// commit together or not at all (plan.md 12), so the payments service calls
// this from within the transaction that records the payment.
func (s *Service) IssueReceipt(ctx context.Context, tx pgx.Tx, actor *users.User, req ReceiptRequest, requestID string) (*Document, error) {
	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, err
	}
	if decision := rules.CanIssueDocumentType(string(TypeReceipt)); !decision.Allowed {
		return nil, &ErrComplianceBlocked{Reason: decision.Reason}
	}

	businessProfile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("read business profile: %w", err)
	}

	// A receipt never carries VAT of its own: it acknowledges money received,
	// and the tax treatment belongs to the document being settled.
	totals, err := ComputeTotals(req.Lines, false, 0)
	if err != nil {
		return nil, err
	}

	testMode := !rules.CanIssueForReal().Allowed
	series := SeriesOfficial
	if testMode {
		series = SeriesTest
	}

	// The row starts as a draft because document_lines refuses an insert onto a
	// document that is already issued; finalize flips it once the lines are in.
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO documents
			(document_type, state, customer_id, activity_id, document_date,
			 subtotal_agorot, vat_agorot, total_agorot, notes, created_by, updated_by)
		VALUES ('RECEIPT', 'DRAFT', $1, $2, $3::date, $4, 0, $5, $6, $7, $7)
		RETURNING id`,
		req.CustomerID, nullableString(derefOrEmpty(req.ActivityID)), req.DocumentDate,
		totals.Subtotal.Agorot(), totals.Total.Agorot(), req.Notes, actor.ID).Scan(&id); err != nil {
		return nil, fmt.Errorf("create receipt: %w", err)
	}

	if err := s.insertLines(ctx, tx, id, totals.Lines); err != nil {
		return nil, err
	}

	document, err := s.repo.Get(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	// Allocate inside this transaction, exactly as the draft flow does
	// (plan.md 10).
	number, err := s.sequences.Allocate(ctx, tx, TypeReceipt, series)
	if err != nil {
		return nil, err
	}

	result, err := s.finalize(ctx, tx, finalizeParams{
		Document: document,
		Lines:    totals.Lines,
		Totals:   totals,
		Business: businessProfile,
		Series:   series,
		Number:   number,
		TestMode: testMode,
		Rules:    rules,
		Actor:    actor,
		Payment:  req.Payment,
	})
	if err != nil {
		return nil, err
	}

	for _, settledID := range req.SettledDocumentIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_relations (from_document_id, to_document_id, relation_type, created_by)
			VALUES ($1, $2, 'RECEIPT_FOR', $3)
			ON CONFLICT DO NOTHING`, id, settledID, actor.ID); err != nil {
			return nil, fmt.Errorf("record receipt relation: %w", err)
		}
	}

	issued, err := s.repo.GetWithLines(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	if err := s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: actor.ID,
		ActorEmail:  actor.Email,
		Operation:   audit.OpDocumentIssued,
		EntityType:  audit.EntityDocument,
		EntityID:    id,
		RequestID:   requestID,
		After: map[string]any{
			"type":         TypeReceipt,
			"full_number":  result.FullNumber,
			"total_agorot": totals.Total.Agorot(),
			"pdf_sha256":   result.SHA256,
			"test_mode":    testMode,
		},
		Reason: complianceNote(testMode),
	}); err != nil {
		return nil, err
	}

	return issued, nil
}

// OutstandingDocument is an issued document with money still owed on it.
type OutstandingDocument struct {
	ID           string       `json:"id"`
	DocumentType Type         `json:"document_type"`
	FullNumber   string       `json:"full_number"`
	DocumentDate string       `json:"document_date"`
	Total        money.Amount `json:"total_agorot"`
	Allocated    money.Amount `json:"allocated_agorot"`
	Outstanding  money.Amount `json:"outstanding_agorot"`
}

// Outstanding lists a customer's issued, uncancelled invoices and payment
// requests that are not yet fully settled, newest first.
//
// Receipts are excluded: a receipt acknowledges money already received and is
// never something a customer owes.
func (s *Service) Outstanding(ctx context.Context, q db.Querier, customerID string) ([]OutstandingDocument, error) {
	rows, err := q.Query(ctx, `
		SELECT d.id::text, d.document_type,
		       d.series || '-' || lpad(d.number::text, 6, '0'),
		       d.document_date, d.total_agorot,
		       coalesce(sum(pa.amount_agorot), 0) AS allocated
		FROM documents d
		LEFT JOIN payment_allocations pa ON pa.document_id = d.id
		WHERE d.customer_id = $1
		  AND d.state = 'ISSUED'
		  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
		GROUP BY d.id
		HAVING d.total_agorot > coalesce(sum(pa.amount_agorot), 0)
		ORDER BY d.document_date DESC, d.number DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	documents := []OutstandingDocument{}
	for rows.Next() {
		var (
			out          OutstandingDocument
			documentDate time.Time
		)
		if err := rows.Scan(&out.ID, &out.DocumentType, &out.FullNumber,
			&documentDate, &out.Total, &out.Allocated); err != nil {
			return nil, err
		}
		out.DocumentDate = documentDate.Format("2006-01-02")
		out.Outstanding = out.Total - out.Allocated
		documents = append(documents, out)
	}
	return documents, rows.Err()
}

// SettleableAmount reports a document's total and how much of it is already
// allocated, locking the row so two concurrent payments cannot both believe
// they are settling the same outstanding balance.
func (s *Service) SettleableAmount(ctx context.Context, tx pgx.Tx, documentID, customerID string) (total, allocated money.Amount, err error) {
	var (
		state        State
		documentType Type
		owner        string
	)
	if err = tx.QueryRow(ctx, `
		SELECT state, document_type, customer_id::text, total_agorot
		FROM documents WHERE id = $1 FOR UPDATE`, documentID).
		Scan(&state, &documentType, &owner, &total); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		return 0, 0, err
	}

	if state != StateIssued {
		return 0, 0, fmt.Errorf("%w: document %s is %s", ErrNotIssued, documentID, state)
	}
	if documentType == TypeReceipt {
		return 0, 0, fmt.Errorf("a receipt cannot be settled by a payment")
	}
	if owner != customerID {
		return 0, 0, fmt.Errorf("document %s belongs to another customer", documentID)
	}

	if err = tx.QueryRow(ctx,
		`SELECT coalesce(sum(amount_agorot), 0) FROM payment_allocations WHERE document_id = $1`,
		documentID).Scan(&allocated); err != nil {
		return 0, 0, err
	}
	return total, allocated, nil
}
