// Package reports answers the questions plan.md 15 asks: what came in, what is
// still owed, what went out, and how close the business is to the exempt-dealer
// ceiling.
//
// Every figure is derived from issued documents and recorded payments. Nothing
// here writes, and nothing here invents a number that the underlying data does
// not support.
package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/compliance"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
)

// Range is a closed business-date interval.
type Range struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RevenueRow is one line of a revenue breakdown.
type RevenueRow struct {
	Key    string       `json:"key"`
	Label  string       `json:"label"`
	Count  int          `json:"count"`
	Amount money.Amount `json:"amount_agorot"`
}

// UnpaidRow is an issued document still owing money (plan.md 15).
type UnpaidRow struct {
	DocumentID   string       `json:"document_id"`
	FullNumber   string       `json:"full_number"`
	DocumentType string       `json:"document_type"`
	CustomerID   string       `json:"customer_id"`
	CustomerName string       `json:"customer_name"`
	DocumentDate string       `json:"document_date"`
	DueDate      *string      `json:"due_date"`
	Total        money.Amount `json:"total_agorot"`
	Paid         money.Amount `json:"paid_agorot"`
	Outstanding  money.Amount `json:"outstanding_agorot"`
	// DaysOverdue is positive once the due date has passed; zero when there is
	// no due date or it has not passed.
	DaysOverdue int `json:"days_overdue"`
}

// Turnover is progress against the exempt-dealer annual ceiling (plan.md 2, 7).
type Turnover struct {
	Year int `json:"year"`
	// Revenue counts issued, uncancelled invoices and payment requests in the
	// year. Receipts are excluded: counting them would double the same money.
	Revenue money.Amount `json:"revenue_agorot"`
	// ThresholdKnown is false when no ceiling is configured for that year. The
	// number is never guessed (plan.md 22.3).
	ThresholdKnown bool         `json:"threshold_known"`
	Threshold      money.Amount `json:"threshold_agorot"`
	Remaining      money.Amount `json:"remaining_agorot"`
	PercentUsed    float64      `json:"percent_used"`
}

// Service computes reports.
type Service struct {
	pool       *db.Pool
	settings   *settings.Service
	compliance *compliance.Service
	location   *time.Location
}

// NewService wires the report service.
func NewService(pool *db.Pool, settingsService *settings.Service, complianceService *compliance.Service, location *time.Location) *Service {
	return &Service{pool: pool, settings: settingsService, compliance: complianceService, location: location}
}

// revenueBase is the set of documents that count as revenue: issued,
// uncancelled invoices and payment requests. A receipt acknowledges money
// already counted by the document it settles, so including it would double
// every figure.
const revenueBase = `
	FROM documents d
	JOIN customers c ON c.id = d.customer_id
	WHERE d.state = 'ISSUED'
	  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')`

// RevenueByPeriod groups revenue by day, month or year.
func (s *Service) RevenueByPeriod(ctx context.Context, r Range, granularity string) ([]RevenueRow, error) {
	truncation := "month"
	switch strings.ToLower(granularity) {
	case "day":
		truncation = "day"
	case "year":
		truncation = "year"
	case "", "month":
		truncation = "month"
	default:
		return nil, fmt.Errorf("unknown granularity %q", granularity)
	}

	// date_trunc's unit cannot be a bind parameter, so it comes from the
	// switch above and never from the request.
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(date_trunc('`+truncation+`', d.document_date), 'YYYY-MM-DD') AS bucket,
		       count(*), coalesce(sum(d.total_agorot), 0)`+revenueBase+`
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		GROUP BY 1
		ORDER BY 1`, nullableString(r.From), nullableString(r.To))
	if err != nil {
		return nil, err
	}
	return scanRevenue(rows, func(key string) string { return key })
}

// RevenueByCustomer groups revenue by customer, largest first.
func (s *Service) RevenueByCustomer(ctx context.Context, r Range) ([]RevenueRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.customer_id::text, count(*), coalesce(sum(d.total_agorot), 0), c.display_name`+revenueBase+`
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		GROUP BY d.customer_id, c.display_name
		ORDER BY 3 DESC`, nullableString(r.From), nullableString(r.To))
	if err != nil {
		return nil, err
	}
	return scanRevenueWithLabel(rows)
}

// RevenueByService groups revenue by catalogue item, using the document lines.
// Free-form lines are grouped together under their own label, because they
// belong to no catalogue item.
func (s *Service) RevenueByService(ctx context.Context, r Range) ([]RevenueRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(dl.service_id::text, 'free-form'),
		       count(*), coalesce(sum(dl.line_total_agorot), 0),
		       coalesce(sv.name, 'שורות חופשיות')
		FROM document_lines dl
		JOIN documents d ON d.id = dl.document_id
		LEFT JOIN services sv ON sv.id = dl.service_id
		WHERE d.state = 'ISSUED'
		  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		GROUP BY 1, 4
		ORDER BY 3 DESC`, nullableString(r.From), nullableString(r.To))
	if err != nil {
		return nil, err
	}
	return scanRevenueWithLabel(rows)
}

// RevenueByActivity groups revenue by activity, so an event's takings can be
// compared with its expenses (plan.md 8).
func (s *Service) RevenueByActivity(ctx context.Context, r Range) ([]RevenueRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(d.activity_id::text, 'none'),
		       count(*), coalesce(sum(d.total_agorot), 0),
		       coalesce(a.name, 'ללא פעילות')
		FROM documents d
		LEFT JOIN activities a ON a.id = d.activity_id
		WHERE d.state = 'ISSUED'
		  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
		  AND ($1::date IS NULL OR d.document_date >= $1)
		  AND ($2::date IS NULL OR d.document_date <= $2)
		GROUP BY 1, 4
		ORDER BY 3 DESC`, nullableString(r.From), nullableString(r.To))
	if err != nil {
		return nil, err
	}
	return scanRevenueWithLabel(rows)
}

// Unpaid lists issued documents that still owe money, most overdue first.
func (s *Service) Unpaid(ctx context.Context, asOf time.Time) ([]UnpaidRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id::text,
		       d.series || '-' || lpad(d.number::text, 6, '0'),
		       d.document_type, d.customer_id::text, c.display_name,
		       d.document_date, d.due_date, d.total_agorot,
		       coalesce(sum(pa.amount_agorot), 0)
		FROM documents d
		JOIN customers c ON c.id = d.customer_id
		LEFT JOIN payment_allocations pa ON pa.document_id = d.id
		WHERE d.state = 'ISSUED'
		  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
		GROUP BY d.id, c.display_name
		HAVING d.total_agorot > coalesce(sum(pa.amount_agorot), 0)
		ORDER BY d.due_date NULLS LAST, d.document_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	today := asOf.In(s.location)
	unpaid := []UnpaidRow{}

	for rows.Next() {
		var (
			row          UnpaidRow
			documentDate time.Time
			dueDate      *time.Time
		)
		if err := rows.Scan(&row.DocumentID, &row.FullNumber, &row.DocumentType,
			&row.CustomerID, &row.CustomerName, &documentDate, &dueDate,
			&row.Total, &row.Paid); err != nil {
			return nil, err
		}

		row.DocumentDate = documentDate.Format("2006-01-02")
		if dueDate != nil {
			formatted := dueDate.Format("2006-01-02")
			row.DueDate = &formatted
			if days := int(today.Sub(*dueDate).Hours() / 24); days > 0 {
				row.DaysOverdue = days
			}
		}
		row.Outstanding = row.Total - row.Paid
		unpaid = append(unpaid, row)
	}
	return unpaid, rows.Err()
}

// AnnualTurnover reports progress against the exempt-dealer ceiling for a year.
func (s *Service) AnnualTurnover(ctx context.Context, year int) (*Turnover, error) {
	turnover := &Turnover{Year: year}

	if err := s.pool.QueryRow(ctx, `
		SELECT coalesce(sum(total_agorot), 0)
		FROM documents
		WHERE state = 'ISSUED'
		  AND document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
		  AND extract(year FROM document_date) = $1`, year).Scan(&turnover.Revenue); err != nil {
		return nil, err
	}

	// The ceiling is configuration, resolved for that year. A year with no
	// configured value reports "unknown" rather than a guessed number.
	businessDate := time.Date(year, 12, 31, 0, 0, 0, 0, s.location)
	threshold, err := s.settings.ExemptTurnoverThreshold(ctx, businessDate)
	if err == nil {
		turnover.ThresholdKnown = true
		turnover.Threshold = money.FromAgorot(threshold.AmountAgorot)
		turnover.Remaining = turnover.Threshold - turnover.Revenue
		if turnover.Threshold > 0 {
			turnover.PercentUsed = float64(turnover.Revenue) / float64(turnover.Threshold) * 100
		}
	}

	return turnover, nil
}

// Today returns the current business date in the business timezone.
func (s *Service) Today() time.Time { return s.compliance.Today() }

func scanRevenue(rows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}, label func(string) string) ([]RevenueRow, error) {
	defer rows.Close()

	result := []RevenueRow{}
	for rows.Next() {
		var row RevenueRow
		if err := rows.Scan(&row.Key, &row.Count, &row.Amount); err != nil {
			return nil, err
		}
		row.Label = label(row.Key)
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanRevenueWithLabel(rows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}) ([]RevenueRow, error) {
	defer rows.Close()

	result := []RevenueRow{}
	for rows.Next() {
		var row RevenueRow
		if err := rows.Scan(&row.Key, &row.Count, &row.Amount, &row.Label); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
