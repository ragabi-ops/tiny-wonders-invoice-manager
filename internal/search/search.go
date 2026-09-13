// Package search answers the global search box: one query, results from every
// entity the operator might be looking for (plan.md 7).
//
// It queries the same trigram-indexed columns the per-entity lists use, in one
// round trip, and returns a small capped slice per kind. It is a navigation
// aid, not a reporting tool: anything that needs filters, paging or totals
// belongs to that entity's own list endpoint.
package search

import (
	"context"
	"strings"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Kind is the entity a hit belongs to.
type Kind string

const (
	KindCustomer Kind = "CUSTOMER"
	KindService  Kind = "SERVICE"
	KindActivity Kind = "ACTIVITY"
)

// perKindLimit caps each section, so one crowded entity cannot bury the others.
const perKindLimit = 5

// MinQueryLength avoids scanning the whole table for a single character.
const MinQueryLength = 2

// Hit is one search result, in the shape the UI needs to render a row and
// navigate to it.
type Hit struct {
	Kind     Kind   `json:"kind"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}

// Results groups hits by kind.
type Results struct {
	Query      string `json:"query"`
	Customers  []Hit  `json:"customers"`
	Services   []Hit  `json:"services"`
	Activities []Hit  `json:"activities"`
}

// Service performs global search.
type Service struct {
	pool *db.Pool
}

// NewService wires the search service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// Search returns matches across customers, services and activities. A query
// shorter than MinQueryLength returns nothing rather than everything.
func (s *Service) Search(ctx context.Context, query string) (*Results, error) {
	query = strings.TrimSpace(query)

	results := &Results{
		Query:      query,
		Customers:  []Hit{},
		Services:   []Hit{},
		Activities: []Hit{},
	}
	if len([]rune(query)) < MinQueryLength {
		return results, nil
	}

	// One statement, so the whole search is a single round trip. Archived
	// customers and services are excluded: the box is for finding what you are
	// working with now, and each entity's own list can show the rest.
	rows, err := s.pool.Query(ctx, `
		(
			SELECT 'CUSTOMER' AS kind, id::text, display_name AS title,
			       btrim(coalesce(nullif(phone, ''), email, '')) AS subtitle,
			       CASE WHEN display_name ILIKE $1 || '%' THEN 0 ELSE 1 END AS rank,
			       display_name AS sort_key
			FROM customers
			WHERE active AND search_text ILIKE '%' || $1 || '%'
			ORDER BY rank, sort_key
			LIMIT $2
		)
		UNION ALL
		(
			SELECT 'SERVICE', id::text, name,
			       btrim(coalesce(nullif(category, ''), '')),
			       CASE WHEN name ILIKE $1 || '%' THEN 0 ELSE 1 END,
			       name
			FROM services
			WHERE active AND search_text ILIKE '%' || $1 || '%'
			ORDER BY 5, 6
			LIMIT $2
		)
		UNION ALL
		(
			SELECT 'ACTIVITY', id::text, name,
			       btrim(coalesce(nullif(location, ''), '')),
			       CASE WHEN name ILIKE $1 || '%' THEN 0 ELSE 1 END,
			       name
			FROM activities
			WHERE search_text ILIKE '%' || $1 || '%'
			ORDER BY 5, 6
			LIMIT $2
		)`, query, perKindLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hit     Hit
			rank    int
			sortKey string
		)
		if err := rows.Scan(&hit.Kind, &hit.ID, &hit.Title, &hit.Subtitle, &rank, &sortKey); err != nil {
			return nil, err
		}

		switch hit.Kind {
		case KindCustomer:
			results.Customers = append(results.Customers, hit)
		case KindService:
			results.Services = append(results.Services, hit)
		case KindActivity:
			results.Activities = append(results.Activities, hit)
		}
	}
	return results, rows.Err()
}
