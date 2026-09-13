// Package payments records money received and the receipts that acknowledge it.
//
// A payment is a record in its own right, and what it settles is a separate set
// of explicit allocations. That is what makes partial payments, split payments
// and payments with no invoice behind them all the same shape rather than three
// special cases (plan.md 12).
package payments

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
)

// Method is how money arrived (plan.md 12).
type Method string

const (
	MethodCash         Method = "CASH"
	MethodBankTransfer Method = "BANK_TRANSFER"
	MethodCreditCard   Method = "CREDIT_CARD"
	MethodBit          Method = "BIT"
	MethodPaybox       Method = "PAYBOX"
	MethodCheck        Method = "CHECK"
	MethodOther        Method = "OTHER"
)

// AllMethods is the closed set, mirroring the database CHECK.
var AllMethods = []Method{
	MethodCash, MethodBankTransfer, MethodCreditCard,
	MethodBit, MethodPaybox, MethodCheck, MethodOther,
}

// Valid reports whether m is a known method.
func (m Method) Valid() bool {
	for _, known := range AllMethods {
		if m == known {
			return true
		}
	}
	return false
}

// HebrewName is the method as displayed and as printed on a receipt.
func (m Method) HebrewName() string {
	switch m {
	case MethodCash:
		return "מזומן"
	case MethodBankTransfer:
		return "העברה בנקאית"
	case MethodCreditCard:
		return "כרטיס אשראי"
	case MethodBit:
		return "ביט"
	case MethodPaybox:
		return "פייבוקס"
	case MethodCheck:
		return "המחאה"
	case MethodOther:
		return "אחר"
	default:
		return string(m)
	}
}

// Sentinel errors the HTTP layer maps to the API error catalogue.
var (
	ErrNotFound         = errors.New("payment not found")
	ErrReceiptExists    = errors.New("payment already has a receipt")
	ErrOverAllocated    = errors.New("allocations exceed the payment amount")
	ErrDocumentOverpaid = errors.New("allocation exceeds what is outstanding on the document")
	ErrCustomerHidden   = errors.New("customer is archived")
)

// Allocation is one document a payment was applied to.
type Allocation struct {
	DocumentID   string       `json:"document_id"`
	FullNumber   string       `json:"full_number"`
	DocumentType string       `json:"document_type"`
	Amount       money.Amount `json:"amount_agorot"`
}

// AllocationInput is an allocation as submitted.
type AllocationInput struct {
	DocumentID string       `json:"document_id"`
	Amount     money.Amount `json:"amount_agorot"`
}

// Payment is money received.
type Payment struct {
	ID           string       `json:"id"`
	CustomerID   string       `json:"customer_id"`
	CustomerName string       `json:"customer_name"`
	Amount       money.Amount `json:"amount_agorot"`
	ReceivedAt   string       `json:"received_at"`
	Method       Method       `json:"method"`
	MethodHebrew string       `json:"method_hebrew"`
	Reference    string       `json:"reference"`
	Notes        string       `json:"notes"`

	ActivityID   *string `json:"activity_id"`
	ActivityName *string `json:"activity_name"`

	ReceiptDocumentID *string `json:"receipt_document_id"`
	ReceiptNumber     *string `json:"receipt_number"`

	// Allocated is how much of this payment settles specific documents; the
	// remainder sits on account.
	Allocated money.Amount `json:"allocated_agorot"`
	OnAccount money.Amount `json:"on_account_agorot"`

	Allocations []Allocation `json:"allocations"`

	CreatedAt time.Time `json:"created_at"`
}

// Input is a payment as submitted.
type Input struct {
	CustomerID  string            `json:"customer_id"`
	Amount      money.Amount      `json:"amount_agorot"`
	ReceivedAt  string            `json:"received_at"`
	Method      Method            `json:"method"`
	Reference   string            `json:"reference"`
	Notes       string            `json:"notes"`
	ActivityID  *string           `json:"activity_id"`
	Allocations []AllocationInput `json:"allocations"`

	// IssueReceipt is the primary flow (plan.md 12) and defaults to true. It can
	// be turned off so money that arrives while the PDF renderer is down is
	// still recorded; the receipt is issued afterwards.
	IssueReceipt *bool `json:"issue_receipt"`
}

// WantsReceipt reports whether a receipt should be issued with the payment.
func (in *Input) WantsReceipt() bool {
	return in.IssueReceipt == nil || *in.IssueReceipt
}

// Normalize trims text and drops empty allocation rows from the form.
func (in *Input) Normalize() {
	in.CustomerID = strings.TrimSpace(in.CustomerID)
	in.Reference = strings.TrimSpace(in.Reference)
	in.Notes = strings.TrimSpace(in.Notes)
	in.ReceivedAt = strings.TrimSpace(in.ReceivedAt)

	kept := in.Allocations[:0]
	for _, allocation := range in.Allocations {
		allocation.DocumentID = strings.TrimSpace(allocation.DocumentID)
		if allocation.DocumentID == "" || allocation.Amount.IsZero() {
			continue
		}
		kept = append(kept, allocation)
	}
	in.Allocations = kept
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
func (in *Input) Validate() map[string]string {
	problems := map[string]string{}

	if in.CustomerID == "" {
		problems["customer_id"] = "יש לבחור לקוח"
	}
	if in.Amount.Agorot() <= 0 {
		problems["amount_agorot"] = "הסכום חייב להיות גדול מאפס"
	}
	if !in.Method.Valid() {
		problems["method"] = "אמצעי תשלום לא מוכר"
	}
	if _, err := time.Parse("2006-01-02", in.ReceivedAt); err != nil {
		problems["received_at"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
	}

	var allocated money.Amount
	for index, allocation := range in.Allocations {
		if allocation.Amount.IsNegative() {
			problems["allocations."+itoa(index)+".amount_agorot"] = "הסכום אינו יכול להיות שלילי"
			continue
		}
		sum, err := allocated.Add(allocation.Amount)
		if err != nil {
			problems["allocations"] = "הסכום גדול מדי"
			break
		}
		allocated = sum
	}

	// Allocating more than arrived would invent money.
	if allocated > in.Amount {
		problems["allocations"] = "סכום ההקצאות גדול מסכום התשלום"
	}
	return problems
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ListParams filters and pages the payment list.
type ListParams struct {
	CustomerID      string
	ActivityID      string
	Method          Method
	FromDate        string
	ToDate          string
	OnlyUnreceipted bool
	Limit           int
	Offset          int
}

// Page is one page of payments plus the total that matched.
type Page struct {
	Payments []Payment `json:"payments"`
	Total    int       `json:"total"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
	// TotalAmount is the sum of every payment matching the filter, not only the
	// page, so the UI can show a meaningful figure alongside the rows.
	TotalAmount money.Amount `json:"total_amount_agorot"`
}

// Repository reads and writes payments.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const paymentColumns = `
	p.id::text, p.customer_id::text, c.display_name, p.amount_agorot, p.received_at,
	p.method, p.reference, p.notes,
	p.activity_id::text, a.name,
	p.receipt_document_id::text,
	CASE WHEN r.number IS NULL THEN NULL
	     ELSE r.series || '-' || lpad(r.number::text, 6, '0') END,
	p.created_at`

const paymentFrom = `
	FROM payments p
	JOIN customers c ON c.id = p.customer_id
	LEFT JOIN activities a ON a.id = p.activity_id
	LEFT JOIN documents r ON r.id = p.receipt_document_id`

func scanPayment(row pgx.Row) (*Payment, error) {
	var (
		p          Payment
		receivedAt time.Time
	)
	if err := row.Scan(
		&p.ID, &p.CustomerID, &p.CustomerName, &p.Amount, &receivedAt,
		&p.Method, &p.Reference, &p.Notes,
		&p.ActivityID, &p.ActivityName,
		&p.ReceiptDocumentID, &p.ReceiptNumber,
		&p.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	p.ReceivedAt = receivedAt.Format("2006-01-02")
	p.MethodHebrew = p.Method.HebrewName()
	p.Allocations = []Allocation{}
	return &p, nil
}

// Get returns one payment with its allocations.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Payment, error) {
	payment, err := scanPayment(q.QueryRow(ctx, `SELECT `+paymentColumns+paymentFrom+` WHERE p.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if err := repo.attachAllocations(ctx, q, payment); err != nil {
		return nil, err
	}
	return payment, nil
}

// GetForUpdate returns one payment and locks its row.
func (repo *Repository) GetForUpdate(ctx context.Context, q db.Querier, id string) (*Payment, error) {
	return scanPayment(q.QueryRow(ctx,
		`SELECT `+paymentColumns+paymentFrom+` WHERE p.id = $1 FOR UPDATE OF p`, id))
}

// List returns a filtered page of payments, newest first.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	args := []any{
		nullableString(p.CustomerID),
		nullableString(p.ActivityID),
		nullableString(string(p.Method)),
		nullableString(p.FromDate),
		nullableString(p.ToDate),
		p.OnlyUnreceipted,
	}

	const where = `
		WHERE ($1::uuid IS NULL OR p.customer_id = $1)
		  AND ($2::uuid IS NULL OR p.activity_id = $2)
		  AND ($3::text IS NULL OR p.method = $3)
		  AND ($4::date IS NULL OR p.received_at >= $4)
		  AND ($5::date IS NULL OR p.received_at <= $5)
		  AND (NOT $6::boolean OR p.receipt_document_id IS NULL)`

	page := &Page{Payments: []Payment{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(p.amount_agorot), 0)`+paymentFrom+where, args...).
		Scan(&page.Total, &page.TotalAmount); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `SELECT `+paymentColumns+paymentFrom+where+`
		ORDER BY p.received_at DESC, p.created_at DESC
		LIMIT $7 OFFSET $8`, append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		payment, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		page.Payments = append(page.Payments, *payment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for index := range page.Payments {
		if err := repo.attachAllocations(ctx, q, &page.Payments[index]); err != nil {
			return nil, err
		}
	}
	return page, nil
}

// attachAllocations fills in a payment's allocations and derived sums.
func (repo *Repository) attachAllocations(ctx context.Context, q db.Querier, payment *Payment) error {
	rows, err := q.Query(ctx, `
		SELECT pa.document_id::text,
		       d.series || '-' || lpad(d.number::text, 6, '0'),
		       d.document_type, pa.amount_agorot
		FROM payment_allocations pa
		JOIN documents d ON d.id = pa.document_id
		WHERE pa.payment_id = $1
		ORDER BY d.document_date, d.number`, payment.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	payment.Allocations = []Allocation{}
	payment.Allocated = 0
	for rows.Next() {
		var allocation Allocation
		if err := rows.Scan(&allocation.DocumentID, &allocation.FullNumber,
			&allocation.DocumentType, &allocation.Amount); err != nil {
			return err
		}
		payment.Allocations = append(payment.Allocations, allocation)
		payment.Allocated += allocation.Amount
	}
	if err := rows.Err(); err != nil {
		return err
	}

	payment.OnAccount = payment.Amount - payment.Allocated
	return nil
}

// Insert stores a payment row.
func (repo *Repository) Insert(ctx context.Context, q db.Querier, in Input, actorID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO payments
			(customer_id, amount_agorot, received_at, method, reference, notes,
			 activity_id, created_by, updated_by)
		VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8, $8)
		RETURNING id::text`,
		in.CustomerID, in.Amount.Agorot(), in.ReceivedAt, string(in.Method),
		in.Reference, in.Notes, nullableString(derefOrEmpty(in.ActivityID)), actorID).Scan(&id)
	return id, err
}

// InsertAllocation links a payment to a document it settles.
func (repo *Repository) InsertAllocation(ctx context.Context, q db.Querier, paymentID, documentID string, amount money.Amount) error {
	_, err := q.Exec(ctx, `
		INSERT INTO payment_allocations (payment_id, document_id, amount_agorot)
		VALUES ($1, $2, $3)`, paymentID, documentID, amount.Agorot())
	return err
}

// AttachReceipt records which receipt acknowledges a payment. This is the last
// write a payment ever accepts: the trigger freezes the row afterwards.
func (repo *Repository) AttachReceipt(ctx context.Context, q db.Querier, paymentID, receiptID, actorID string) error {
	tag, err := q.Exec(ctx,
		`UPDATE payments SET receipt_document_id = $2, updated_by = $3 WHERE id = $1`,
		paymentID, receiptID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Balance is what a customer owes and has paid (plan.md 8).
type Balance struct {
	CustomerID string `json:"customer_id"`
	// Invoiced is the total of issued, uncancelled invoices and payment requests.
	Invoiced money.Amount `json:"invoiced_agorot"`
	// Paid is every payment received from this customer.
	Paid money.Amount `json:"paid_agorot"`
	// Allocated is how much of those payments settles specific documents.
	Allocated money.Amount `json:"allocated_agorot"`
	// OnAccount is money received that settles nothing in particular.
	OnAccount money.Amount `json:"on_account_agorot"`
	// Open is what is still owed on documents: invoiced minus allocated. Money
	// on account is deliberately not netted off, because it has not been
	// applied to anything and the operator may still choose where it goes.
	Open money.Amount `json:"open_agorot"`
}

// CustomerBalance computes a customer's position.
func (repo *Repository) CustomerBalance(ctx context.Context, q db.Querier, customerID string) (*Balance, error) {
	balance := &Balance{CustomerID: customerID}

	if err := q.QueryRow(ctx, `
		SELECT coalesce(sum(total_agorot), 0)
		FROM documents
		WHERE customer_id = $1
		  AND state = 'ISSUED'
		  AND document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')`, customerID).
		Scan(&balance.Invoiced); err != nil {
		return nil, err
	}

	if err := q.QueryRow(ctx, `
		SELECT coalesce(sum(amount_agorot), 0) FROM payments WHERE customer_id = $1`, customerID).
		Scan(&balance.Paid); err != nil {
		return nil, err
	}

	if err := q.QueryRow(ctx, `
		SELECT coalesce(sum(pa.amount_agorot), 0)
		FROM payment_allocations pa
		JOIN payments p ON p.id = pa.payment_id
		WHERE p.customer_id = $1`, customerID).Scan(&balance.Allocated); err != nil {
		return nil, err
	}

	balance.OnAccount = balance.Paid - balance.Allocated
	balance.Open = balance.Invoiced - balance.Allocated
	return balance, nil
}

// TimelineEntry is one dated event on a customer's history (plan.md 8).
type TimelineEntry struct {
	Kind string `json:"kind"` // DOCUMENT or PAYMENT
	ID   string `json:"id"`
	Date string `json:"date"`

	Title      string       `json:"title"`
	FullNumber string       `json:"full_number"`
	Amount     money.Amount `json:"amount_agorot"`
	State      string       `json:"state"`
	Detail     string       `json:"detail"`
}

// CustomerTimeline returns a customer's documents and payments, newest first.
func (repo *Repository) CustomerTimeline(ctx context.Context, q db.Querier, customerID string, limit int) ([]TimelineEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	rows, err := q.Query(ctx, `
		(
			SELECT 'DOCUMENT' AS kind, d.id::text, d.document_date AS date,
			       d.document_type AS title,
			       coalesce(d.series || '-' || lpad(d.number::text, 6, '0'), '') AS full_number,
			       d.total_agorot AS amount, d.state AS state, '' AS detail,
			       d.created_at
			FROM documents d
			WHERE d.customer_id = $1
		)
		UNION ALL
		(
			SELECT 'PAYMENT', p.id::text, p.received_at,
			       p.method,
			       coalesce(r.series || '-' || lpad(r.number::text, 6, '0'), ''),
			       p.amount_agorot,
			       CASE WHEN p.receipt_document_id IS NULL THEN 'UNRECEIPTED' ELSE 'RECEIPTED' END,
			       p.reference,
			       p.created_at
			FROM payments p
			LEFT JOIN documents r ON r.id = p.receipt_document_id
			WHERE p.customer_id = $1
		)
		ORDER BY date DESC, created_at DESC
		LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []TimelineEntry{}
	for rows.Next() {
		var (
			entry     TimelineEntry
			date      time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&entry.Kind, &entry.ID, &date, &entry.Title,
			&entry.FullNumber, &entry.Amount, &entry.State, &entry.Detail, &createdAt); err != nil {
			return nil, err
		}
		entry.Date = date.Format("2006-01-02")
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// MethodTotal is one row of the payments-by-method report (plan.md 15).
type MethodTotal struct {
	Method       Method       `json:"method"`
	MethodHebrew string       `json:"method_hebrew"`
	Count        int          `json:"count"`
	Total        money.Amount `json:"total_agorot"`
}

// TotalsByMethod summarizes payments in a date range.
func (repo *Repository) TotalsByMethod(ctx context.Context, q db.Querier, fromDate, toDate string) ([]MethodTotal, error) {
	rows, err := q.Query(ctx, `
		SELECT method, count(*), coalesce(sum(amount_agorot), 0)
		FROM payments
		WHERE ($1::date IS NULL OR received_at >= $1)
		  AND ($2::date IS NULL OR received_at <= $2)
		GROUP BY method
		ORDER BY sum(amount_agorot) DESC`,
		nullableString(fromDate), nullableString(toDate))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := []MethodTotal{}
	for rows.Next() {
		var total MethodTotal
		if err := rows.Scan(&total.Method, &total.Count, &total.Total); err != nil {
			return nil, err
		}
		total.MethodHebrew = total.Method.HebrewName()
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
