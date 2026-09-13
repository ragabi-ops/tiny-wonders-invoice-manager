// Package expenses records what the business spends, with the receipt photo or
// supplier invoice attached.
//
// This is bookkeeping support for the owner and their accountant, not a
// double-entry engine, and it deliberately computes no VAT input deduction: an
// exempt dealer has none (plan.md 13).
package expenses

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/payments"
)

// ErrNotFound means no expense has that identifier.
var ErrNotFound = errors.New("expense not found")

// Attachment is a stored file belonging to an expense.
type Attachment struct {
	ID               string    `json:"id"`
	OriginalFilename string    `json:"original_filename"`
	ContentType      string    `json:"content_type"`
	ByteSize         int64     `json:"byte_size"`
	SHA256           string    `json:"sha256"`
	UploadedAt       time.Time `json:"uploaded_at"`
}

// Expense is one thing the business paid for.
type Expense struct {
	ID          string       `json:"id"`
	Supplier    string       `json:"supplier"`
	ExpenseDate string       `json:"expense_date"`
	Amount      money.Amount `json:"amount_agorot"`
	Category    string       `json:"category"`

	PaymentMethod payments.Method `json:"payment_method"`
	MethodHebrew  string          `json:"payment_method_hebrew"`
	Reference     string          `json:"reference"`
	Notes         string          `json:"notes"`

	ActivityID   *string `json:"activity_id"`
	ActivityName *string `json:"activity_name"`

	Attachments []Attachment `json:"attachments"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Input is the mutable part of an expense.
type Input struct {
	Supplier      string          `json:"supplier"`
	ExpenseDate   string          `json:"expense_date"`
	Amount        money.Amount    `json:"amount_agorot"`
	Category      string          `json:"category"`
	PaymentMethod payments.Method `json:"payment_method"`
	Reference     string          `json:"reference"`
	Notes         string          `json:"notes"`
	ActivityID    *string         `json:"activity_id"`
}

// Normalize trims text and fills in the default method.
func (in *Input) Normalize() {
	in.Supplier = strings.TrimSpace(in.Supplier)
	in.Category = strings.TrimSpace(in.Category)
	in.Reference = strings.TrimSpace(in.Reference)
	in.Notes = strings.TrimSpace(in.Notes)
	in.ExpenseDate = strings.TrimSpace(in.ExpenseDate)

	if in.PaymentMethod == "" {
		in.PaymentMethod = payments.MethodOther
	}
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
func (in *Input) Validate() map[string]string {
	problems := map[string]string{}

	if in.Supplier == "" {
		problems["supplier"] = "יש להזין ספק"
	}
	if in.Amount.Agorot() <= 0 {
		problems["amount_agorot"] = "הסכום חייב להיות גדול מאפס"
	}
	if _, err := time.Parse("2006-01-02", in.ExpenseDate); err != nil {
		problems["expense_date"] = "יש להזין תאריך בפורמט YYYY-MM-DD"
	}
	if !in.PaymentMethod.Valid() {
		problems["payment_method"] = "אמצעי תשלום לא מוכר"
	}
	return problems
}

// ListParams filters and pages the expense list.
type ListParams struct {
	Query      string
	Category   string
	ActivityID string
	Method     payments.Method
	FromDate   string
	ToDate     string
	Limit      int
	Offset     int
}

// Page is one page of expenses plus the totals that matched.
type Page struct {
	Expenses    []Expense    `json:"expenses"`
	Total       int          `json:"total"`
	Limit       int          `json:"limit"`
	Offset      int          `json:"offset"`
	TotalAmount money.Amount `json:"total_amount_agorot"`
}

// CategoryTotal is one row of the expenses-by-category report (plan.md 15).
type CategoryTotal struct {
	Category string       `json:"category"`
	Count    int          `json:"count"`
	Total    money.Amount `json:"total_agorot"`
}

// Repository reads and writes expenses.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const expenseColumns = `
	e.id::text, e.supplier, e.expense_date, e.amount_agorot, e.category,
	e.payment_method, e.reference, e.notes,
	e.activity_id::text, a.name,
	e.created_at, e.updated_at`

const expenseFrom = `
	FROM expenses e
	LEFT JOIN activities a ON a.id = e.activity_id`

func scanExpense(row pgx.Row) (*Expense, error) {
	var (
		expense     Expense
		expenseDate time.Time
	)
	if err := row.Scan(
		&expense.ID, &expense.Supplier, &expenseDate, &expense.Amount, &expense.Category,
		&expense.PaymentMethod, &expense.Reference, &expense.Notes,
		&expense.ActivityID, &expense.ActivityName,
		&expense.CreatedAt, &expense.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	expense.ExpenseDate = expenseDate.Format("2006-01-02")
	expense.MethodHebrew = expense.PaymentMethod.HebrewName()
	expense.Attachments = []Attachment{}
	return &expense, nil
}

// Get returns one expense with its attachments.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Expense, error) {
	expense, err := scanExpense(q.QueryRow(ctx, `SELECT `+expenseColumns+expenseFrom+` WHERE e.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if expense.Attachments, err = repo.Attachments(ctx, q, id); err != nil {
		return nil, err
	}
	return expense, nil
}

// List returns a filtered page of expenses, newest first.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	args := []any{
		nullableString(p.Query),
		nullableString(p.Category),
		nullableString(p.ActivityID),
		nullableString(string(p.Method)),
		nullableString(p.FromDate),
		nullableString(p.ToDate),
	}

	const where = `
		WHERE ($1::text IS NULL OR e.search_text ILIKE '%' || $1 || '%')
		  AND ($2::text IS NULL OR e.category = $2)
		  AND ($3::uuid IS NULL OR e.activity_id = $3)
		  AND ($4::text IS NULL OR e.payment_method = $4)
		  AND ($5::date IS NULL OR e.expense_date >= $5)
		  AND ($6::date IS NULL OR e.expense_date <= $6)`

	page := &Page{Expenses: []Expense{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(e.amount_agorot), 0)`+expenseFrom+where, args...).
		Scan(&page.Total, &page.TotalAmount); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `SELECT `+expenseColumns+expenseFrom+where+`
		ORDER BY e.expense_date DESC, e.created_at DESC
		LIMIT $7 OFFSET $8`, append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		expense, err := scanExpense(rows)
		if err != nil {
			return nil, err
		}
		page.Expenses = append(page.Expenses, *expense)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for index := range page.Expenses {
		attachments, err := repo.Attachments(ctx, q, page.Expenses[index].ID)
		if err != nil {
			return nil, err
		}
		page.Expenses[index].Attachments = attachments
	}
	return page, nil
}

// Attachments returns the files belonging to an expense.
func (repo *Repository) Attachments(ctx context.Context, q db.Querier, expenseID string) ([]Attachment, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, original_filename, content_type, byte_size, sha256, uploaded_at
		FROM attachments
		WHERE entity_type = 'expense' AND entity_id = $1
		ORDER BY uploaded_at`, expenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attachments := []Attachment{}
	for rows.Next() {
		var attachment Attachment
		if err := rows.Scan(&attachment.ID, &attachment.OriginalFilename, &attachment.ContentType,
			&attachment.ByteSize, &attachment.SHA256, &attachment.UploadedAt); err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

// AttachmentRef locates a stored attachment and the hash it must still match.
type AttachmentRef struct {
	Path             string
	SHA256           string
	ContentType      string
	OriginalFilename string
}

// Attachment returns where an attachment is stored.
func (repo *Repository) Attachment(ctx context.Context, q db.Querier, id string) (*AttachmentRef, error) {
	var ref AttachmentRef
	err := q.QueryRow(ctx, `
		SELECT storage_path, sha256, content_type, original_filename
		FROM attachments WHERE id = $1`, id).
		Scan(&ref.Path, &ref.SHA256, &ref.ContentType, &ref.OriginalFilename)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ref, nil
}

// Create inserts an expense.
func (repo *Repository) Create(ctx context.Context, q db.Querier, in Input, actorID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO expenses
			(supplier, expense_date, amount_agorot, category, payment_method,
			 reference, notes, activity_id, created_by, updated_by)
		VALUES ($1, $2::date, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING id::text`,
		in.Supplier, in.ExpenseDate, in.Amount.Agorot(), in.Category,
		string(in.PaymentMethod), in.Reference, in.Notes,
		nullableString(derefOrEmpty(in.ActivityID)), actorID).Scan(&id)
	return id, err
}

// Update replaces the mutable fields of an expense.
func (repo *Repository) Update(ctx context.Context, q db.Querier, id string, in Input, actorID string) error {
	tag, err := q.Exec(ctx, `
		UPDATE expenses
		SET supplier = $2, expense_date = $3::date, amount_agorot = $4, category = $5,
		    payment_method = $6, reference = $7, notes = $8, activity_id = $9, updated_by = $10
		WHERE id = $1`,
		id, in.Supplier, in.ExpenseDate, in.Amount.Agorot(), in.Category,
		string(in.PaymentMethod), in.Reference, in.Notes,
		nullableString(derefOrEmpty(in.ActivityID)), actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes an expense. Unlike an issued document, an expense is an
// internal record with no official number, so a mistake is corrected by
// deleting it — audited, of course.
func (repo *Repository) Delete(ctx context.Context, q db.Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM expenses WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InsertAttachment records an uploaded file.
func (repo *Repository) InsertAttachment(ctx context.Context, q db.Querier,
	expenseID, filename, contentType, sha256, path string, size int64, actorID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO attachments
			(entity_type, entity_id, original_filename, content_type, byte_size,
			 sha256, storage_path, uploaded_by)
		VALUES ('expense', $1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text`,
		expenseID, filename, contentType, size, sha256, path, actorID).Scan(&id)
	return id, err
}

// NewAttachmentID reserves an identifier so the storage path can be built
// before the row exists.
func (repo *Repository) NewAttachmentID(ctx context.Context, q db.Querier) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id)
	return id, err
}

// InsertAttachmentWithID records an uploaded file under a reserved identifier.
func (repo *Repository) InsertAttachmentWithID(ctx context.Context, q db.Querier, id,
	expenseID, filename, contentType, sha256, path string, size int64, actorID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO attachments
			(id, entity_type, entity_id, original_filename, content_type, byte_size,
			 sha256, storage_path, uploaded_by)
		VALUES ($1, 'expense', $2, $3, $4, $5, $6, $7, $8)`,
		id, expenseID, filename, contentType, size, sha256, path, actorID)
	return err
}

// DeleteAttachment removes an attachment row and returns where its file was, so
// the caller can remove it after the transaction commits.
func (repo *Repository) DeleteAttachment(ctx context.Context, q db.Querier, id string) (string, error) {
	var path string
	err := q.QueryRow(ctx, `DELETE FROM attachments WHERE id = $1 RETURNING storage_path`, id).Scan(&path)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return path, nil
}

// Categories returns the categories already in use.
func (repo *Repository) Categories(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT category FROM expenses WHERE category <> '' ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	categories := []string{}
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}
	return categories, rows.Err()
}

// TotalsByCategory summarizes expenses in a date range (plan.md 15).
func (repo *Repository) TotalsByCategory(ctx context.Context, q db.Querier, fromDate, toDate, activityID string) ([]CategoryTotal, error) {
	rows, err := q.Query(ctx, `
		SELECT coalesce(nullif(category, ''), 'ללא קטגוריה'), count(*), coalesce(sum(amount_agorot), 0)
		FROM expenses
		WHERE ($1::date IS NULL OR expense_date >= $1)
		  AND ($2::date IS NULL OR expense_date <= $2)
		  AND ($3::uuid IS NULL OR activity_id = $3)
		GROUP BY 1
		ORDER BY 3 DESC`,
		nullableString(fromDate), nullableString(toDate), nullableString(activityID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := []CategoryTotal{}
	for rows.Next() {
		var total CategoryTotal
		if err := rows.Scan(&total.Category, &total.Count, &total.Total); err != nil {
			return nil, err
		}
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
