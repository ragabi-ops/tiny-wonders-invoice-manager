// Package catalog owns the reusable service catalogue: the items an operator
// picks from when building a document. Documents may also carry free-form
// lines, so a catalogue item is a convenience, never a requirement
// (plan.md 8).
package catalog

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
)

// ErrNotFound means no catalogue item has that identifier.
var ErrNotFound = errors.New("service not found")

// Item is one catalogue entry. The price is a default: a document line may
// override it, because the server recalculates every total anyway.
type Item struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	DefaultPrice money.Amount `json:"default_price_agorot"`
	Unit         string       `json:"unit"`
	Category     string       `json:"category"`
	Active       bool         `json:"active"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Input is the mutable part of a catalogue item.
type Input struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	DefaultPrice money.Amount `json:"default_price_agorot"`
	Unit         string       `json:"unit"`
	Category     string       `json:"category"`
}

// Normalize trims whitespace.
func (in *Input) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Unit = strings.TrimSpace(in.Unit)
	in.Category = strings.TrimSpace(in.Category)
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
func (in *Input) Validate() map[string]string {
	problems := map[string]string{}

	if in.Name == "" {
		problems["name"] = "יש להזין שם שירות"
	}
	// A negative catalogue price is always a data-entry error; a discount is a
	// document-line concern, not a catalogue one.
	if in.DefaultPrice.IsNegative() {
		problems["default_price_agorot"] = "המחיר אינו יכול להיות שלילי"
	}
	return problems
}

// ListParams filters and pages the catalogue.
type ListParams struct {
	Query         string
	Category      string
	IncludeHidden bool
	Limit         int
	Offset        int
}

// Page is one page of catalogue items plus the total that matched.
type Page struct {
	Services []Item `json:"services"`
	Total    int    `json:"total"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

// Repository reads and writes catalogue items.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const itemColumns = `
	id, name, description, default_price_agorot, unit, category, active,
	created_at, updated_at`

func scanItem(row pgx.Row) (*Item, error) {
	var item Item
	if err := row.Scan(
		&item.ID, &item.Name, &item.Description, &item.DefaultPrice,
		&item.Unit, &item.Category, &item.Active, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &item, nil
}

// Get returns one catalogue item.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Item, error) {
	return scanItem(q.QueryRow(ctx, `SELECT `+itemColumns+` FROM services WHERE id = $1`, id))
}

// List returns a filtered page of catalogue items.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	var queryArg, categoryArg any
	if query := strings.TrimSpace(p.Query); query != "" {
		queryArg = query
	}
	if category := strings.TrimSpace(p.Category); category != "" {
		categoryArg = category
	}

	page := &Page{Services: []Item{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM services
		WHERE ($1::boolean OR active)
		  AND ($2::text IS NULL OR search_text ILIKE '%' || $2 || '%')
		  AND ($3::text IS NULL OR category = $3)`,
		p.IncludeHidden, queryArg, categoryArg).Scan(&page.Total); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT `+itemColumns+`
		FROM services
		WHERE ($1::boolean OR active)
		  AND ($2::text IS NULL OR search_text ILIKE '%' || $2 || '%')
		  AND ($3::text IS NULL OR category = $3)
		ORDER BY category, name
		LIMIT $4 OFFSET $5`,
		p.IncludeHidden, queryArg, categoryArg, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		page.Services = append(page.Services, *item)
	}
	return page, rows.Err()
}

// Categories returns the distinct categories in use, so the UI offers what
// already exists instead of inviting a new spelling of the same word.
func (repo *Repository) Categories(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT DISTINCT category FROM services WHERE category <> '' ORDER BY category`)
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

// Create inserts a catalogue item.
func (repo *Repository) Create(ctx context.Context, q db.Querier, in Input, actorID string) (*Item, error) {
	return scanItem(q.QueryRow(ctx, `
		INSERT INTO services
			(name, description, default_price_agorot, unit, category, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		RETURNING `+itemColumns,
		in.Name, in.Description, in.DefaultPrice.Agorot(), in.Unit, in.Category, actorID))
}

// Update replaces the mutable fields of a catalogue item.
func (repo *Repository) Update(ctx context.Context, q db.Querier, id string, in Input, actorID string) (*Item, error) {
	return scanItem(q.QueryRow(ctx, `
		UPDATE services
		SET name = $2, description = $3, default_price_agorot = $4,
		    unit = $5, category = $6, updated_by = $7
		WHERE id = $1
		RETURNING `+itemColumns,
		id, in.Name, in.Description, in.DefaultPrice.Agorot(), in.Unit, in.Category, actorID))
}

// SetActive hides or restores a catalogue item. Items are never deleted:
// documents reference the price that was used at the time.
func (repo *Repository) SetActive(ctx context.Context, q db.Querier, id string, active bool, actorID string) (*Item, error) {
	return scanItem(q.QueryRow(ctx, `
		UPDATE services SET active = $2, updated_by = $3
		WHERE id = $1
		RETURNING `+itemColumns, id, active, actorID))
}
