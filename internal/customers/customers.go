// Package customers owns customer records: the people and businesses invoices
// are issued to. A customer's details are copied onto every document at
// issuance, so editing a customer never changes a document already issued
// (plan.md 11).
package customers

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Type distinguishes a private person from a business.
type Type string

const (
	TypePerson   Type = "PERSON"
	TypeBusiness Type = "BUSINESS"
)

// Valid reports whether t is a known customer type.
func (t Type) Valid() bool { return t == TypePerson || t == TypeBusiness }

// Delivery is how this customer prefers to receive documents. The channels are
// the ones v1 supports (plan.md 14).
type Delivery string

const (
	DeliveryEmail    Delivery = "EMAIL"
	DeliveryWhatsApp Delivery = "WHATSAPP"
	DeliveryNone     Delivery = "NONE"
)

// Valid reports whether d is a known delivery preference.
func (d Delivery) Valid() bool {
	return d == DeliveryEmail || d == DeliveryWhatsApp || d == DeliveryNone
}

// Sentinel errors the HTTP layer maps to the API error catalogue.
var (
	ErrNotFound     = errors.New("customer not found")
	ErrInvalidType  = errors.New("invalid customer type")
	ErrInvalidDeliv = errors.New("invalid delivery preference")
)

// Customer is a person or business that receives documents.
type Customer struct {
	ID                 string    `json:"id"`
	CustomerType       Type      `json:"customer_type"`
	DisplayName        string    `json:"display_name"`
	LegalName          string    `json:"legal_name"`
	BusinessOrIDNumber string    `json:"business_or_id_number"`
	Phone              string    `json:"phone"`
	Email              string    `json:"email"`
	Address            string    `json:"address"`
	Notes              string    `json:"notes"`
	PreferredDelivery  Delivery  `json:"preferred_delivery"`
	Active             bool      `json:"active"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Input is the mutable part of a customer, shared by create and update.
type Input struct {
	CustomerType       Type     `json:"customer_type"`
	DisplayName        string   `json:"display_name"`
	LegalName          string   `json:"legal_name"`
	BusinessOrIDNumber string   `json:"business_or_id_number"`
	Phone              string   `json:"phone"`
	Email              string   `json:"email"`
	Address            string   `json:"address"`
	Notes              string   `json:"notes"`
	PreferredDelivery  Delivery `json:"preferred_delivery"`
}

// Normalize trims whitespace and fills in the defaults an empty form omits.
func (in *Input) Normalize() {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.LegalName = strings.TrimSpace(in.LegalName)
	in.BusinessOrIDNumber = strings.TrimSpace(in.BusinessOrIDNumber)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Address = strings.TrimSpace(in.Address)
	in.Notes = strings.TrimSpace(in.Notes)

	if in.CustomerType == "" {
		in.CustomerType = TypePerson
	}
	if in.PreferredDelivery == "" {
		in.PreferredDelivery = DeliveryNone
	}
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
// Only the display name is required: demanding more would slow down the common
// case of adding a customer mid-conversation (plan.md 8).
func (in *Input) Validate() map[string]string {
	problems := map[string]string{}

	if in.DisplayName == "" {
		problems["display_name"] = "יש להזין שם"
	}
	if !in.CustomerType.Valid() {
		problems["customer_type"] = "סוג לקוח לא מוכר"
	}
	if !in.PreferredDelivery.Valid() {
		problems["preferred_delivery"] = "אמצעי משלוח לא מוכר"
	}
	if in.Email != "" && !looksLikeEmail(in.Email) {
		problems["email"] = "כתובת הדוא״ל אינה תקינה"
	}
	if in.PreferredDelivery == DeliveryEmail && in.Email == "" {
		problems["email"] = "יש להזין דוא״ל כאשר אמצעי המשלוח המועדף הוא דוא״ל"
	}
	if in.PreferredDelivery == DeliveryWhatsApp && DigitsOnly(in.Phone) == "" {
		problems["phone"] = "יש להזין טלפון כאשר אמצעי המשלוח המועדף הוא וואטסאפ"
	}
	return problems
}

// emailPattern is deliberately loose. The only authoritative test of an address
// is delivering to it, so this rejects obvious typos and nothing more.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func looksLikeEmail(s string) bool { return emailPattern.MatchString(s) }

var nonDigits = regexp.MustCompile(`[^0-9]`)

// DigitsOnly strips formatting from a phone number so that "050-123-4567" and
// "0501234567" compare equal.
func DigitsOnly(phone string) string { return nonDigits.ReplaceAllString(phone, "") }

// ListParams filters and pages the customer list.
type ListParams struct {
	Query         string
	IncludeHidden bool
	Limit         int
	Offset        int
}

// Page is one page of customers plus the total that matched.
type Page struct {
	Customers []Customer `json:"customers"`
	Total     int        `json:"total"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
}

// Repository reads and writes customers.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const customerColumns = `
	id, customer_type, display_name, legal_name, business_or_id_number,
	phone, email, address, notes, preferred_delivery, active,
	created_at, updated_at`

func scanCustomer(row pgx.Row) (*Customer, error) {
	var c Customer
	if err := row.Scan(
		&c.ID, &c.CustomerType, &c.DisplayName, &c.LegalName, &c.BusinessOrIDNumber,
		&c.Phone, &c.Email, &c.Address, &c.Notes, &c.PreferredDelivery, &c.Active,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// Get returns one customer.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Customer, error) {
	return scanCustomer(q.QueryRow(ctx, `SELECT `+customerColumns+` FROM customers WHERE id = $1`, id))
}

// List returns a filtered page of customers, ordered by relevance when a query
// is given and by name otherwise.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	query := strings.TrimSpace(p.Query)
	var queryArg any
	if query != "" {
		queryArg = query
	}

	page := &Page{Customers: []Customer{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM customers
		WHERE ($1::boolean OR active)
		  AND ($2::text IS NULL OR search_text ILIKE '%' || $2 || '%')`,
		p.IncludeHidden, queryArg).Scan(&page.Total); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT `+customerColumns+`
		FROM customers
		WHERE ($1::boolean OR active)
		  AND ($2::text IS NULL OR search_text ILIKE '%' || $2 || '%')
		ORDER BY
			-- An exact prefix match is what the operator usually meant.
			CASE WHEN $2::text IS NOT NULL AND display_name ILIKE $2 || '%' THEN 0 ELSE 1 END,
			display_name
		LIMIT $3 OFFSET $4`,
		p.IncludeHidden, queryArg, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		page.Customers = append(page.Customers, *c)
	}
	return page, rows.Err()
}

// Create inserts a customer.
func (repo *Repository) Create(ctx context.Context, q db.Querier, in Input, actorID string) (*Customer, error) {
	return scanCustomer(q.QueryRow(ctx, `
		INSERT INTO customers
			(customer_type, display_name, legal_name, business_or_id_number,
			 phone, email, address, notes, preferred_delivery, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
		RETURNING `+customerColumns,
		in.CustomerType, in.DisplayName, in.LegalName, in.BusinessOrIDNumber,
		in.Phone, in.Email, in.Address, in.Notes, in.PreferredDelivery, actorID))
}

// Update replaces the mutable fields of a customer.
func (repo *Repository) Update(ctx context.Context, q db.Querier, id string, in Input, actorID string) (*Customer, error) {
	return scanCustomer(q.QueryRow(ctx, `
		UPDATE customers
		SET customer_type = $2, display_name = $3, legal_name = $4,
		    business_or_id_number = $5, phone = $6, email = $7, address = $8,
		    notes = $9, preferred_delivery = $10, updated_by = $11
		WHERE id = $1
		RETURNING `+customerColumns,
		id, in.CustomerType, in.DisplayName, in.LegalName, in.BusinessOrIDNumber,
		in.Phone, in.Email, in.Address, in.Notes, in.PreferredDelivery, actorID))
}

// SetActive hides or restores a customer. Customers are never deleted: they are
// referenced by documents that must stay readable forever.
func (repo *Repository) SetActive(ctx context.Context, q db.Querier, id string, active bool, actorID string) (*Customer, error) {
	return scanCustomer(q.QueryRow(ctx, `
		UPDATE customers SET active = $2, updated_by = $3
		WHERE id = $1
		RETURNING `+customerColumns, id, active, actorID))
}

// DuplicateReason says why a candidate looks like a duplicate.
type DuplicateReason string

const (
	ReasonPhone          DuplicateReason = "PHONE"
	ReasonEmail          DuplicateReason = "EMAIL"
	ReasonBusinessNumber DuplicateReason = "BUSINESS_NUMBER"
	ReasonName           DuplicateReason = "NAME"
)

// HebrewReason explains the match to the operator.
func (r DuplicateReason) HebrewReason() string {
	switch r {
	case ReasonPhone:
		return "אותו מספר טלפון"
	case ReasonEmail:
		return "אותה כתובת דוא״ל"
	case ReasonBusinessNumber:
		return "אותו מספר עוסק או ת״ז"
	case ReasonName:
		return "שם דומה"
	default:
		return string(r)
	}
}

// Duplicate is a customer that resembles the one being entered.
type Duplicate struct {
	Customer Customer        `json:"customer"`
	Reason   DuplicateReason `json:"reason"`
	Hebrew   string          `json:"reason_hebrew"`
}

// FindDuplicates looks for existing customers matching the input. excludeID is
// the customer being edited, so it never reports itself.
//
// This warns; it never blocks. Two family members can share a phone number, and
// a unique constraint would force the operator to falsify data to get past it
// (plan.md 8).
func (repo *Repository) FindDuplicates(ctx context.Context, q db.Querier, in Input, excludeID string) ([]Duplicate, error) {
	phoneDigits := DigitsOnly(in.Phone)

	var excludeArg any
	if excludeID != "" {
		excludeArg = excludeID
	}

	rows, err := q.Query(ctx, `
		SELECT `+customerColumns+`,
			CASE
				WHEN $1::text <> '' AND phone_digits = $1 THEN 'PHONE'
				WHEN $2::text <> '' AND lower(email) = $2 THEN 'EMAIL'
				WHEN $3::text <> '' AND business_or_id_number = $3 THEN 'BUSINESS_NUMBER'
				ELSE 'NAME'
			END AS reason
		FROM customers
		WHERE ($5::uuid IS NULL OR id <> $5)
		  AND (
			($1::text <> '' AND phone_digits = $1)
			OR ($2::text <> '' AND lower(email) = $2)
			OR ($3::text <> '' AND business_or_id_number = $3)
			-- Similarity catches transpositions and missing spaces that an
			-- equality test would miss.
			OR similarity(display_name, $4) > 0.6
		  )
		ORDER BY display_name
		LIMIT 10`,
		phoneDigits, strings.ToLower(in.Email), in.BusinessOrIDNumber, in.DisplayName, excludeArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := []Duplicate{}
	for rows.Next() {
		var d Duplicate
		var reason string
		if err := rows.Scan(
			&d.Customer.ID, &d.Customer.CustomerType, &d.Customer.DisplayName,
			&d.Customer.LegalName, &d.Customer.BusinessOrIDNumber, &d.Customer.Phone,
			&d.Customer.Email, &d.Customer.Address, &d.Customer.Notes,
			&d.Customer.PreferredDelivery, &d.Customer.Active,
			&d.Customer.CreatedAt, &d.Customer.UpdatedAt, &reason,
		); err != nil {
			return nil, err
		}
		d.Reason = DuplicateReason(reason)
		d.Hebrew = d.Reason.HebrewReason()
		duplicates = append(duplicates, d)
	}
	return duplicates, rows.Err()
}
