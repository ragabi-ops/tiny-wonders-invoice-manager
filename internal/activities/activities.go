// Package activities owns the optional operational dimension: an event, market
// day or project that documents, payments and expenses can be attributed to,
// so profitability can be reported per activity. It is deliberately not a
// booking or ticketing system (plan.md 8, 21).
package activities

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Status is where an activity stands.
type Status string

const (
	StatusPlanned   Status = "PLANNED"
	StatusActive    Status = "ACTIVE"
	StatusDone      Status = "DONE"
	StatusCancelled Status = "CANCELLED"
)

// AllStatuses is the closed set, mirroring the database CHECK.
var AllStatuses = []Status{StatusPlanned, StatusActive, StatusDone, StatusCancelled}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	for _, known := range AllStatuses {
		if s == known {
			return true
		}
	}
	return false
}

// HebrewName is the status as shown in the UI.
func (s Status) HebrewName() string {
	switch s {
	case StatusPlanned:
		return "מתוכננת"
	case StatusActive:
		return "פעילה"
	case StatusDone:
		return "הסתיימה"
	case StatusCancelled:
		return "בוטלה"
	default:
		return string(s)
	}
}

// ErrNotFound means no activity has that identifier.
var ErrNotFound = errors.New("activity not found")

// Activity is one operational event.
type Activity struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	StartAt   *time.Time `json:"start_at"`
	EndAt     *time.Time `json:"end_at"`
	Location  string     `json:"location"`
	Status    Status     `json:"status"`
	Notes     string     `json:"notes"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Input is the mutable part of an activity.
type Input struct {
	Name     string     `json:"name"`
	StartAt  *time.Time `json:"start_at"`
	EndAt    *time.Time `json:"end_at"`
	Location string     `json:"location"`
	Status   Status     `json:"status"`
	Notes    string     `json:"notes"`
}

// Normalize trims whitespace and fills in the default status.
func (in *Input) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Location = strings.TrimSpace(in.Location)
	in.Notes = strings.TrimSpace(in.Notes)

	if in.Status == "" {
		in.Status = StatusPlanned
	}
}

// Validate returns field-name to Hebrew-message pairs for anything invalid.
func (in *Input) Validate() map[string]string {
	problems := map[string]string{}

	if in.Name == "" {
		problems["name"] = "יש להזין שם פעילות"
	}
	if !in.Status.Valid() {
		problems["status"] = "סטטוס לא מוכר"
	}
	// The database enforces this too; checking here produces a Hebrew field
	// error instead of a constraint violation.
	if in.StartAt != nil && in.EndAt != nil && in.EndAt.Before(*in.StartAt) {
		problems["end_at"] = "מועד הסיום אינו יכול להקדים את מועד ההתחלה"
	}
	return problems
}

// ListParams filters and pages activities.
type ListParams struct {
	Query  string
	Status Status
	Limit  int
	Offset int
}

// Page is one page of activities plus the total that matched.
type Page struct {
	Activities []Activity `json:"activities"`
	Total      int        `json:"total"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
}

// Repository reads and writes activities.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const activityColumns = `
	id, name, start_at, end_at, location, status, notes, created_at, updated_at`

func scanActivity(row pgx.Row) (*Activity, error) {
	var a Activity
	if err := row.Scan(
		&a.ID, &a.Name, &a.StartAt, &a.EndAt, &a.Location,
		&a.Status, &a.Notes, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

// Get returns one activity.
func (repo *Repository) Get(ctx context.Context, q db.Querier, id string) (*Activity, error) {
	return scanActivity(q.QueryRow(ctx, `SELECT `+activityColumns+` FROM activities WHERE id = $1`, id))
}

// List returns a filtered page of activities, most recent first.
func (repo *Repository) List(ctx context.Context, q db.Querier, p ListParams) (*Page, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	var queryArg, statusArg any
	if query := strings.TrimSpace(p.Query); query != "" {
		queryArg = query
	}
	if p.Status != "" {
		statusArg = string(p.Status)
	}

	page := &Page{Activities: []Activity{}, Limit: p.Limit, Offset: p.Offset}

	if err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM activities
		WHERE ($1::text IS NULL OR search_text ILIKE '%' || $1 || '%')
		  AND ($2::text IS NULL OR status = $2)`,
		queryArg, statusArg).Scan(&page.Total); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT `+activityColumns+`
		FROM activities
		WHERE ($1::text IS NULL OR search_text ILIKE '%' || $1 || '%')
		  AND ($2::text IS NULL OR status = $2)
		ORDER BY start_at DESC NULLS LAST, name
		LIMIT $3 OFFSET $4`,
		queryArg, statusArg, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		activity, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		page.Activities = append(page.Activities, *activity)
	}
	return page, rows.Err()
}

// Create inserts an activity.
func (repo *Repository) Create(ctx context.Context, q db.Querier, in Input, actorID string) (*Activity, error) {
	return scanActivity(q.QueryRow(ctx, `
		INSERT INTO activities (name, start_at, end_at, location, status, notes, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		RETURNING `+activityColumns,
		in.Name, in.StartAt, in.EndAt, in.Location, in.Status, in.Notes, actorID))
}

// Update replaces the mutable fields of an activity.
func (repo *Repository) Update(ctx context.Context, q db.Querier, id string, in Input, actorID string) (*Activity, error) {
	return scanActivity(q.QueryRow(ctx, `
		UPDATE activities
		SET name = $2, start_at = $3, end_at = $4, location = $5,
		    status = $6, notes = $7, updated_by = $8
		WHERE id = $1
		RETURNING `+activityColumns,
		id, in.Name, in.StartAt, in.EndAt, in.Location, in.Status, in.Notes, actorID))
}
