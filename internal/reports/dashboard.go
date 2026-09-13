package reports

import (
	"context"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
)

// RecentDocument is a compact document row for the dashboard.
type RecentDocument struct {
	ID           string       `json:"id"`
	FullNumber   string       `json:"full_number"`
	DocumentType string       `json:"document_type"`
	CustomerName string       `json:"customer_name"`
	DocumentDate string       `json:"document_date"`
	Total        money.Amount `json:"total_agorot"`
	State        string       `json:"state"`
}

// RecentPayment is a compact payment row for the dashboard.
type RecentPayment struct {
	ID           string       `json:"id"`
	CustomerName string       `json:"customer_name"`
	ReceivedAt   string       `json:"received_at"`
	Amount       money.Amount `json:"amount_agorot"`
	Method       string       `json:"method"`
}

// Dashboard is everything the home screen shows (plan.md 7). Each figure is
// derived from real data; nothing is a placeholder.
type Dashboard struct {
	BusinessDate string `json:"business_date"`

	RevenueToday money.Amount `json:"revenue_today_agorot"`
	RevenueMonth money.Amount `json:"revenue_month_agorot"`
	RevenueYear  money.Amount `json:"revenue_year_agorot"`

	UnpaidCount  int          `json:"unpaid_count"`
	UnpaidAmount money.Amount `json:"unpaid_amount_agorot"`
	OverdueCount int          `json:"overdue_count"`

	ExpensesMonth money.Amount `json:"expenses_month_agorot"`

	FailedDeliveries int `json:"failed_deliveries"`
	FailedJobs       int `json:"failed_jobs"`

	Turnover *Turnover `json:"turnover"`

	RecentDocuments []RecentDocument `json:"recent_documents"`
	RecentPayments  []RecentPayment  `json:"recent_payments"`
}

// Dashboard assembles the home screen in one call, so the UI does not fan out
// into a dozen requests on every page load.
func (s *Service) Dashboard(ctx context.Context) (*Dashboard, error) {
	today := s.Today()

	dashboard := &Dashboard{
		BusinessDate:    today.Format("2006-01-02"),
		RecentDocuments: []RecentDocument{},
		RecentPayments:  []RecentPayment{},
	}

	startOfMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, s.location)
	startOfYear := time.Date(today.Year(), 1, 1, 0, 0, 0, 0, s.location)

	if err := s.pool.QueryRow(ctx, `
		SELECT
			coalesce(sum(total_agorot) FILTER (WHERE document_date = $1), 0),
			coalesce(sum(total_agorot) FILTER (WHERE document_date >= $2), 0),
			coalesce(sum(total_agorot) FILTER (WHERE document_date >= $3), 0)
		FROM documents
		WHERE state = 'ISSUED'
		  AND document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')`,
		today.Format("2006-01-02"),
		startOfMonth.Format("2006-01-02"),
		startOfYear.Format("2006-01-02")).
		Scan(&dashboard.RevenueToday, &dashboard.RevenueMonth, &dashboard.RevenueYear); err != nil {
		return nil, err
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(outstanding), 0),
		       count(*) FILTER (WHERE due_date IS NOT NULL AND due_date < $1)
		FROM (
			SELECT d.id, d.due_date,
			       d.total_agorot - coalesce(sum(pa.amount_agorot), 0) AS outstanding
			FROM documents d
			LEFT JOIN payment_allocations pa ON pa.document_id = d.id
			WHERE d.state = 'ISSUED'
			  AND d.document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST')
			GROUP BY d.id
			HAVING d.total_agorot > coalesce(sum(pa.amount_agorot), 0)
		) AS open_documents`, today.Format("2006-01-02")).
		Scan(&dashboard.UnpaidCount, &dashboard.UnpaidAmount, &dashboard.OverdueCount); err != nil {
		return nil, err
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT coalesce(sum(amount_agorot), 0)
		FROM expenses WHERE expense_date >= $1`, startOfMonth.Format("2006-01-02")).
		Scan(&dashboard.ExpensesMonth); err != nil {
		return nil, err
	}

	// A delivery that failed is something the operator has to act on, so it is
	// on the home screen rather than buried (plan.md 7).
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM delivery_attempts WHERE state = 'FAILED'`).
		Scan(&dashboard.FailedDeliveries); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM background_jobs WHERE state = 'FAILED'`).
		Scan(&dashboard.FailedJobs); err != nil {
		return nil, err
	}

	turnover, err := s.AnnualTurnover(ctx, today.Year())
	if err != nil {
		return nil, err
	}
	dashboard.Turnover = turnover

	documentRows, err := s.pool.Query(ctx, `
		SELECT d.id::text,
		       coalesce(d.series || '-' || lpad(d.number::text, 6, '0'), ''),
		       d.document_type, c.display_name, d.document_date, d.total_agorot, d.state
		FROM documents d
		JOIN customers c ON c.id = d.customer_id
		ORDER BY d.created_at DESC
		LIMIT 5`)
	if err != nil {
		return nil, err
	}
	defer documentRows.Close()

	for documentRows.Next() {
		var (
			row          RecentDocument
			documentDate time.Time
		)
		if err := documentRows.Scan(&row.ID, &row.FullNumber, &row.DocumentType,
			&row.CustomerName, &documentDate, &row.Total, &row.State); err != nil {
			return nil, err
		}
		row.DocumentDate = documentDate.Format("2006-01-02")
		dashboard.RecentDocuments = append(dashboard.RecentDocuments, row)
	}
	if err := documentRows.Err(); err != nil {
		return nil, err
	}

	paymentRows, err := s.pool.Query(ctx, `
		SELECT p.id::text, c.display_name, p.received_at, p.amount_agorot, p.method
		FROM payments p
		JOIN customers c ON c.id = p.customer_id
		ORDER BY p.created_at DESC
		LIMIT 5`)
	if err != nil {
		return nil, err
	}
	defer paymentRows.Close()

	for paymentRows.Next() {
		var (
			row        RecentPayment
			receivedAt time.Time
		)
		if err := paymentRows.Scan(&row.ID, &row.CustomerName, &receivedAt,
			&row.Amount, &row.Method); err != nil {
			return nil, err
		}
		row.ReceivedAt = receivedAt.Format("2006-01-02")
		dashboard.RecentPayments = append(dashboard.RecentPayments, row)
	}
	return dashboard, paymentRows.Err()
}
