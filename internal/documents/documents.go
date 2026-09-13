// Package documents owns transaction invoices, payment requests and receipts:
// their drafts, their totals, the atomic issuance that gives them an official
// number, and the immutable snapshot and PDF that issuance produces.
//
// The guarantees this package must never weaken (plan.md 22.5):
//
//   - no two issued documents share a number within a type and series
//   - an issued document is never edited or deleted
//   - totals are computed by the server, never accepted from a client
//   - an issued document always has its snapshot and its stored PDF
package documents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
)

// Type is a document kind (plan.md 9).
type Type string

const (
	TypeTransactionInvoice Type = "TRANSACTION_INVOICE" // חשבונית עסקה
	TypePaymentRequest     Type = "PAYMENT_REQUEST"     // דרישת תשלום
	TypeReceipt            Type = "RECEIPT"             // קבלה
)

// AllTypes is the closed set, mirroring the database CHECK.
var AllTypes = []Type{TypeTransactionInvoice, TypePaymentRequest, TypeReceipt}

// Valid reports whether t is a known document type.
func (t Type) Valid() bool {
	for _, known := range AllTypes {
		if t == known {
			return true
		}
	}
	return false
}

// HebrewName is the document's title as printed and displayed.
func (t Type) HebrewName() string {
	switch t {
	case TypeTransactionInvoice:
		return "חשבונית עסקה"
	case TypePaymentRequest:
		return "דרישת תשלום"
	case TypeReceipt:
		return "קבלה"
	default:
		return string(t)
	}
}

// State is where a document stands (plan.md 9).
type State string

const (
	StateDraft     State = "DRAFT"
	StateIssued    State = "ISSUED"
	StateCancelled State = "CANCELLED"
)

// HebrewName is the state as displayed.
func (s State) HebrewName() string {
	switch s {
	case StateDraft:
		return "טיוטה"
	case StateIssued:
		return "הופק"
	case StateCancelled:
		return "בוטל"
	default:
		return string(s)
	}
}

// Series separates official numbering from the numbering used while the
// compliance gate is closed, so a test document can never consume an official
// number (plan.md 2, 10).
const (
	SeriesOfficial = "A"
	SeriesTest     = "TEST"
)

// Sentinel errors the HTTP layer maps to the API error catalogue.
var (
	ErrNotFound       = errors.New("document not found")
	ErrNotDraft       = errors.New("only a draft can be edited")
	ErrAlreadyIssued  = errors.New("document is already issued")
	ErrNotIssued      = errors.New("only an issued document can be cancelled")
	ErrAlreadyVoid    = errors.New("document is already cancelled")
	ErrNoLines        = errors.New("a document must have at least one line")
	ErrCustomerHidden = errors.New("customer is archived")
)

// Line is one row of a document.
type Line struct {
	ID            string       `json:"id"`
	LineNumber    int          `json:"line_number"`
	ServiceID     *string      `json:"service_id"`
	Description   string       `json:"description"`
	Unit          string       `json:"unit"`
	QuantityMilli int64        `json:"quantity_milli"`
	UnitPrice     money.Amount `json:"unit_price_agorot"`
	LineTotal     money.Amount `json:"line_total_agorot"`
}

// LineInput is a line as submitted. The line total is absent on purpose: the
// server computes it (plan.md 3.2).
type LineInput struct {
	ServiceID     *string      `json:"service_id"`
	Description   string       `json:"description"`
	Unit          string       `json:"unit"`
	QuantityMilli int64        `json:"quantity_milli"`
	UnitPrice     money.Amount `json:"unit_price_agorot"`
}

// Document is a draft, issued or cancelled document.
type Document struct {
	ID           string `json:"id"`
	DocumentType Type   `json:"document_type"`
	State        State  `json:"state"`

	Series string `json:"series"`
	Number *int64 `json:"number"`
	// FullNumber is the number as printed, e.g. "A-000123". Empty for a draft.
	FullNumber string `json:"full_number"`

	CustomerID   string  `json:"customer_id"`
	CustomerName string  `json:"customer_name"`
	ActivityID   *string `json:"activity_id"`
	ActivityName *string `json:"activity_name"`

	DocumentDate string  `json:"document_date"`
	DueDate      *string `json:"due_date"`

	Currency string       `json:"currency"`
	Subtotal money.Amount `json:"subtotal_agorot"`
	VAT      money.Amount `json:"vat_agorot"`
	Total    money.Amount `json:"total_agorot"`

	Notes           string `json:"notes"`
	RequiredWording string `json:"required_wording"`

	IssuedAt *time.Time `json:"issued_at"`
	IssuedBy *string    `json:"issued_by"`

	CancelledAt        *time.Time `json:"cancelled_at"`
	CancellationReason *string    `json:"cancellation_reason"`

	PDFSHA256       *string `json:"pdf_sha256"`
	PDFBytes        *int64  `json:"pdf_bytes"`
	TemplateVersion *string `json:"template_version"`

	TestMode bool `json:"test_mode"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Lines []Line `json:"lines"`
}

// Editable reports whether the document may still be changed.
func (d *Document) Editable() bool { return d.State == StateDraft }

// DraftInput is the mutable part of a draft.
type DraftInput struct {
	DocumentType Type        `json:"document_type"`
	CustomerID   string      `json:"customer_id"`
	ActivityID   *string     `json:"activity_id"`
	DocumentDate string      `json:"document_date"`
	DueDate      *string     `json:"due_date"`
	Notes        string      `json:"notes"`
	Lines        []LineInput `json:"lines"`
}

// Normalize trims text and drops blank trailing lines the UI may have sent.
func (in *DraftInput) Normalize() {
	in.Notes = strings.TrimSpace(in.Notes)
	in.CustomerID = strings.TrimSpace(in.CustomerID)

	kept := in.Lines[:0]
	for _, line := range in.Lines {
		line.Description = strings.TrimSpace(line.Description)
		line.Unit = strings.TrimSpace(line.Unit)
		// A line with no description and no quantity is an empty row from the
		// form, not something the operator meant to charge for.
		if line.Description == "" && line.QuantityMilli == 0 && line.UnitPrice.IsZero() {
			continue
		}
		kept = append(kept, line)
	}
	in.Lines = kept
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
func (in *DraftInput) Validate() map[string]string {
	problems := map[string]string{}

	if !in.DocumentType.Valid() {
		problems["document_type"] = "סוג מסמך לא מוכר"
	}
	if in.CustomerID == "" {
		problems["customer_id"] = "יש לבחור לקוח"
	}
	if _, err := ParseBusinessDate(in.DocumentDate); err != nil {
		problems["document_date"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
	}
	if in.DueDate != nil && *in.DueDate != "" {
		if _, err := ParseBusinessDate(*in.DueDate); err != nil {
			problems["due_date"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
		}
	}
	if len(in.Lines) == 0 {
		problems["lines"] = "יש להוסיף לפחות שורה אחת"
	}

	for index, line := range in.Lines {
		field := "lines." + itoa(index)
		switch {
		case strings.TrimSpace(line.Description) == "":
			problems[field+".description"] = "יש להזין תיאור"
		case line.QuantityMilli <= 0:
			problems[field+".quantity_milli"] = "הכמות חייבת להיות גדולה מאפס"
		case line.UnitPrice.IsNegative():
			problems[field+".unit_price_agorot"] = "המחיר אינו יכול להיות שלילי"
		}
	}
	return problems
}

// ParseBusinessDate reads a YYYY-MM-DD business date.
func ParseBusinessDate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(value))
}

// FormatNumber renders an official number as it is printed: series, dash,
// six zero-padded digits.
func FormatNumber(series string, number int64) string {
	return series + "-" + pad6(number)
}

func pad6(n int64) string {
	digits := itoa64(n)
	for len(digits) < 6 {
		digits = "0" + digits
	}
	return digits
}

func itoa(n int) string { return itoa64(int64(n)) }

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Totals is the server's computation of a document's money.
type Totals struct {
	Lines    []Line
	Subtotal money.Amount
	VAT      money.Amount
	Total    money.Amount
}

// ComputeTotals prices every line and sums them. The client's own idea of the
// totals is never consulted; this is the only place they come from
// (plan.md 3.2).
//
// vatApplies comes from the compliance adapter. In exempt-dealer mode it is
// always false and the VAT line is zero (plan.md 2).
func ComputeTotals(inputs []LineInput, vatApplies bool, vatRateBasisPoints int64) (*Totals, error) {
	if len(inputs) == 0 {
		return nil, ErrNoLines
	}

	totals := &Totals{Lines: make([]Line, 0, len(inputs))}

	for index, input := range inputs {
		lineTotal, err := input.UnitPrice.MulQuantityMilli(input.QuantityMilli)
		if err != nil {
			return nil, err
		}

		totals.Lines = append(totals.Lines, Line{
			LineNumber:    index + 1,
			ServiceID:     input.ServiceID,
			Description:   strings.TrimSpace(input.Description),
			Unit:          strings.TrimSpace(input.Unit),
			QuantityMilli: input.QuantityMilli,
			UnitPrice:     input.UnitPrice,
			LineTotal:     lineTotal,
		})

		subtotal, err := totals.Subtotal.Add(lineTotal)
		if err != nil {
			return nil, err
		}
		totals.Subtotal = subtotal
	}

	if vatApplies && vatRateBasisPoints > 0 {
		// Not reachable in v1: exempt dealers charge no VAT. The branch exists
		// so the shape is right when authorized-dealer mode is enabled, and it
		// is driven by configuration rather than a constant.
		vat, err := totals.Subtotal.MulQuantityMilli(vatRateBasisPoints * money.QuantityScale / 10000)
		if err != nil {
			return nil, err
		}
		totals.VAT = vat
	}

	total, err := totals.Subtotal.Add(totals.VAT)
	if err != nil {
		return nil, err
	}
	totals.Total = total

	return totals, nil
}

// ListParams filters and pages the document list.
type ListParams struct {
	Query        string
	CustomerID   string
	ActivityID   string
	DocumentType Type
	State        State
	FromDate     string
	ToDate       string
	Limit        int
	Offset       int
}

// Page is one page of documents plus the total that matched.
type Page struct {
	Documents []Document `json:"documents"`
	Total     int        `json:"total"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
}

// Snapshot is the immutable record of what a document said when it was issued.
// A historical document is rendered from this and never from the mutable
// customer, business or catalogue tables (plan.md 11).
type Snapshot struct {
	SchemaVersion int `json:"schema_version"`

	Business struct {
		LegalName      string `json:"legal_name"`
		DisplayName    string `json:"display_name"`
		BusinessNumber string `json:"business_number"`
		Address        string `json:"address"`
		Phone          string `json:"phone"`
		Email          string `json:"email"`
		Website        string `json:"website"`
		BankDetails    string `json:"bank_details"`
		FooterNote     string `json:"footer_note"`
		// LogoPath and LogoSHA256 pin the logo file as it was at issuance.
		// Logo files are never overwritten, so re-rendering this document years
		// later prints the mark it was actually issued with (plan.md 11).
		LogoPath   string `json:"logo_path,omitempty"`
		LogoSHA256 string `json:"logo_sha256,omitempty"`
	} `json:"business"`

	Customer struct {
		ID                 string `json:"id"`
		CustomerType       string `json:"customer_type"`
		DisplayName        string `json:"display_name"`
		LegalName          string `json:"legal_name"`
		BusinessOrIDNumber string `json:"business_or_id_number"`
		Phone              string `json:"phone"`
		Email              string `json:"email"`
		Address            string `json:"address"`
	} `json:"customer"`

	Activity *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"activity,omitempty"`

	Document struct {
		Type            string `json:"type"`
		TypeHebrew      string `json:"type_hebrew"`
		Series          string `json:"series"`
		Number          int64  `json:"number"`
		FullNumber      string `json:"full_number"`
		DocumentDate    string `json:"document_date"`
		DueDate         string `json:"due_date,omitempty"`
		Currency        string `json:"currency"`
		Notes           string `json:"notes"`
		RequiredWording string `json:"required_wording"`
		TestMode        bool   `json:"test_mode"`
	} `json:"document"`

	Lines []struct {
		LineNumber    int    `json:"line_number"`
		Description   string `json:"description"`
		Unit          string `json:"unit"`
		QuantityMilli int64  `json:"quantity_milli"`
		UnitPrice     int64  `json:"unit_price_agorot"`
		LineTotal     int64  `json:"line_total_agorot"`
	} `json:"lines"`

	Totals struct {
		Subtotal int64 `json:"subtotal_agorot"`
		VAT      int64 `json:"vat_agorot"`
		Total    int64 `json:"total_agorot"`
	} `json:"totals"`

	// Payment is present on a receipt: what was received, how, and against
	// which documents. A receipt that did not state this would be useless as
	// evidence of payment (plan.md 11, 12).
	Payment *PaymentSnapshot `json:"payment,omitempty"`

	IssuedAt        time.Time `json:"issued_at"`
	IssuedByEmail   string    `json:"issued_by_email"`
	TemplateVersion string    `json:"template_version"`
	// BusinessMode records which tax regime produced this document, so a later
	// reader knows why there is no VAT line.
	BusinessMode string `json:"business_mode"`
}

// PaymentSnapshot is the payment a receipt acknowledges, captured at issuance.
type PaymentSnapshot struct {
	ID           string `json:"id"`
	AmountAgorot int64  `json:"amount_agorot"`
	ReceivedAt   string `json:"received_at"`
	Method       string `json:"method"`
	MethodHebrew string `json:"method_hebrew"`
	Reference    string `json:"reference"`
	// SettledDocuments lists what this payment was applied to. Empty when the
	// payment is entirely on account.
	SettledDocuments []SettledDocument `json:"settled_documents,omitempty"`
	// OnAccountAgorot is the part of the payment not applied to any document.
	OnAccountAgorot int64 `json:"on_account_agorot"`
}

// SettledDocument is one document a payment was applied to.
type SettledDocument struct {
	DocumentID   string `json:"document_id"`
	TypeHebrew   string `json:"type_hebrew"`
	FullNumber   string `json:"full_number"`
	AmountAgorot int64  `json:"amount_agorot"`
}

// Repository reads and writes documents.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const documentColumns = `
	d.id, d.document_type, d.state, d.series, d.number,
	d.customer_id, c.display_name, d.activity_id, a.name,
	d.document_date, d.due_date, d.currency,
	d.subtotal_agorot, d.vat_agorot, d.total_agorot,
	d.notes, d.required_wording,
	d.issued_at, d.issued_by::text,
	d.cancelled_at, d.cancellation_reason,
	d.pdf_sha256, d.pdf_bytes, d.template_version,
	d.test_mode, d.created_at, d.updated_at`

const documentFrom = `
	FROM documents d
	JOIN customers c ON c.id = d.customer_id
	LEFT JOIN activities a ON a.id = d.activity_id`

func scanDocument(row pgx.Row) (*Document, error) {
	var d Document
	var documentDate time.Time
	var dueDate *time.Time

	if err := row.Scan(
		&d.ID, &d.DocumentType, &d.State, &d.Series, &d.Number,
		&d.CustomerID, &d.CustomerName, &d.ActivityID, &d.ActivityName,
		&documentDate, &dueDate, &d.Currency,
		&d.Subtotal, &d.VAT, &d.Total,
		&d.Notes, &d.RequiredWording,
		&d.IssuedAt, &d.IssuedBy,
		&d.CancelledAt, &d.CancellationReason,
		&d.PDFSHA256, &d.PDFBytes, &d.TemplateVersion,
		&d.TestMode, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	d.DocumentDate = documentDate.Format("2006-01-02")
	if dueDate != nil {
		formatted := dueDate.Format("2006-01-02")
		d.DueDate = &formatted
	}
	if d.Number != nil {
		d.FullNumber = FormatNumber(d.Series, *d.Number)
	}
	d.Lines = []Line{}
	return &d, nil
}

// Get returns one document without its lines.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Document, error) {
	return scanDocument(q.QueryRow(ctx, `SELECT `+documentColumns+documentFrom+` WHERE d.id = $1`, id))
}

// GetForUpdate returns one document and locks its row, so a concurrent issue or
// cancel of the same document serializes behind this one.
func (repo *Repository) GetForUpdate(ctx context.Context, q db.Querier, id string) (*Document, error) {
	return scanDocument(q.QueryRow(ctx,
		`SELECT `+documentColumns+documentFrom+` WHERE d.id = $1 FOR UPDATE OF d`, id))
}

// GetWithLines returns one document and its lines.
func (repo *Repository) GetWithLines(ctx context.Context, q db.Querier, id string) (*Document, error) {
	document, err := repo.Get(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if document.Lines, err = repo.Lines(ctx, q, id); err != nil {
		return nil, err
	}
	return document, nil
}

// Lines returns a document's lines in order.
func (repo *Repository) Lines(ctx context.Context, q db.Querier, documentID string) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT id, line_number, service_id::text, description, unit,
		       quantity_milli, unit_price_agorot, line_total_agorot
		FROM document_lines
		WHERE document_id = $1
		ORDER BY line_number`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []Line{}
	for rows.Next() {
		var line Line
		if err := rows.Scan(&line.ID, &line.LineNumber, &line.ServiceID, &line.Description,
			&line.Unit, &line.QuantityMilli, &line.UnitPrice, &line.LineTotal); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// List returns a filtered page of documents, newest first.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	args := []any{
		nullableString(p.Query),
		nullableString(p.CustomerID),
		nullableString(p.ActivityID),
		nullableString(string(p.DocumentType)),
		nullableString(string(p.State)),
		nullableString(p.FromDate),
		nullableString(p.ToDate),
	}

	const where = `
		WHERE ($1::text IS NULL OR c.display_name ILIKE '%' || $1 || '%'
		       OR coalesce(d.series || '-' || lpad(d.number::text, 6, '0'), '') ILIKE '%' || $1 || '%')
		  AND ($2::uuid IS NULL OR d.customer_id = $2)
		  AND ($3::uuid IS NULL OR d.activity_id = $3)
		  AND ($4::text IS NULL OR d.document_type = $4)
		  AND ($5::text IS NULL OR d.state = $5)
		  AND ($6::date IS NULL OR d.document_date >= $6)
		  AND ($7::date IS NULL OR d.document_date <= $7)`

	page := &Page{Documents: []Document{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx, `SELECT count(*)`+documentFrom+where, args...).Scan(&page.Total); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `SELECT `+documentColumns+documentFrom+where+`
		ORDER BY d.document_date DESC, d.created_at DESC
		LIMIT $8 OFFSET $9`, append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		document, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		page.Documents = append(page.Documents, *document)
	}
	return page, rows.Err()
}

// SnapshotJSON returns the raw stored snapshot of an issued document.
func (repo *Repository) SnapshotJSON(ctx context.Context, q db.Querier, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := q.QueryRow(ctx, `SELECT snapshot FROM documents WHERE id = $1`, id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return raw, nil
}

// ArtifactRef locates a stored PDF and the hash it must still match.
type ArtifactRef struct {
	Path       string
	SHA256     string
	FullNumber string
	TestMode   bool
}

// Artifact returns where an issued document's PDF is stored.
func (repo *Repository) Artifact(ctx context.Context, q db.Querier, id string) (*ArtifactRef, error) {
	var (
		ref    ArtifactRef
		path   *string
		sha    *string
		series string
		number *int64
	)
	err := q.QueryRow(ctx,
		`SELECT pdf_path, pdf_sha256, series, number, test_mode FROM documents WHERE id = $1`, id).
		Scan(&path, &sha, &series, &number, &ref.TestMode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if path == nil || sha == nil || number == nil {
		return nil, ErrNotIssued
	}

	ref.Path, ref.SHA256 = *path, *sha
	ref.FullNumber = FormatNumber(series, *number)
	return &ref, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
