package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Service performs payment use cases.
type Service struct {
	pool      *db.Pool
	repo      *Repository
	documents *documents.Service
	audit     *audit.Recorder
}

// NewService wires the payment service.
func NewService(pool *db.Pool, repo *Repository, documentService *documents.Service, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, repo: repo, documents: documentService, audit: recorder}
}

// List returns a page of payments.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one payment with its allocations.
func (s *Service) Get(ctx context.Context, id string) (*Payment, error) {
	return s.repo.Get(ctx, s.pool, id)
}

// CustomerBalance returns what a customer owes and has paid.
func (s *Service) CustomerBalance(ctx context.Context, customerID string) (*Balance, error) {
	return s.repo.CustomerBalance(ctx, s.pool, customerID)
}

// CustomerTimeline returns a customer's documents and payments, newest first.
func (s *Service) CustomerTimeline(ctx context.Context, customerID string, limit int) ([]TimelineEntry, error) {
	return s.repo.CustomerTimeline(ctx, s.pool, customerID, limit)
}

// Outstanding lists the customer's documents that still owe money, so the UI
// can offer them when allocating a payment.
func (s *Service) Outstanding(ctx context.Context, customerID string) ([]documents.OutstandingDocument, error) {
	return s.documents.Outstanding(ctx, s.pool, customerID)
}

// TotalsByMethod summarizes payments in a date range.
func (s *Service) TotalsByMethod(ctx context.Context, fromDate, toDate string) ([]MethodTotal, error) {
	return s.repo.TotalsByMethod(ctx, s.pool, fromDate, toDate)
}

// Create records a payment and, in the primary flow, issues its receipt — all
// in one transaction, so the two can never disagree (plan.md 12).
//
// The whole of plan.md 12's flow lives here: validate, persist the payment,
// record the allocations, allocate the receipt number atomically, create the
// immutable receipt, render and store its PDF, audit, commit.
func (s *Service) Create(ctx context.Context, actor *users.User, in Input, requestID string) (*Payment, error) {
	in.Normalize()

	var created *Payment
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.assertCustomerUsable(ctx, tx, in.CustomerID); err != nil {
			return err
		}

		paymentID, err := s.repo.Insert(ctx, tx, in, actor.ID)
		if err != nil {
			return err
		}

		settled, err := s.applyAllocations(ctx, tx, paymentID, in)
		if err != nil {
			return err
		}

		if err := s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpPaymentRecorded,
			EntityType:  audit.EntityPayment,
			EntityID:    paymentID,
			RequestID:   requestID,
			After: map[string]any{
				"amount_agorot": in.Amount.Agorot(),
				"method":        in.Method,
				"received_at":   in.ReceivedAt,
				"allocations":   len(settled),
			},
		}); err != nil {
			return err
		}

		if in.WantsReceipt() {
			if err := s.issueReceiptTx(ctx, tx, actor, paymentID, requestID); err != nil {
				return err
			}
		}

		created, err = s.repo.Get(ctx, tx, paymentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// IssueReceipt issues the receipt for a payment that does not have one yet —
// the case where money was recorded while the renderer was unavailable.
func (s *Service) IssueReceipt(ctx context.Context, actor *users.User, paymentID, requestID string) (*Payment, error) {
	var updated *Payment

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		payment, err := s.repo.GetForUpdate(ctx, tx, paymentID)
		if err != nil {
			return err
		}
		if payment.ReceiptDocumentID != nil {
			return ErrReceiptExists
		}

		if err := s.issueReceiptTx(ctx, tx, actor, paymentID, requestID); err != nil {
			return err
		}

		updated, err = s.repo.Get(ctx, tx, paymentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// PreviewReceipt renders the receipt this payment would produce, without
// recording anything.
func (s *Service) PreviewReceipt(ctx context.Context, in Input) ([]byte, string, error) {
	in.Normalize()

	// Resolve the allocations against real documents so the preview names them
	// exactly as the issued receipt would, without locking or writing anything.
	allocations := make([]Allocation, 0, len(in.Allocations))
	var allocated money.Amount

	for _, allocation := range in.Allocations {
		var (
			fullNumber   string
			documentType string
		)
		if err := s.pool.QueryRow(ctx, `
			SELECT coalesce(series || '-' || lpad(number::text, 6, '0'), ''), document_type
			FROM documents WHERE id = $1`, allocation.DocumentID).
			Scan(&fullNumber, &documentType); err != nil {
			return nil, "", fmt.Errorf("%w: %s", documents.ErrNotFound, allocation.DocumentID)
		}

		allocations = append(allocations, Allocation{
			DocumentID:   allocation.DocumentID,
			FullNumber:   fullNumber,
			DocumentType: documentType,
			Amount:       allocation.Amount,
		})

		sum, err := allocated.Add(allocation.Amount)
		if err != nil {
			return nil, "", err
		}
		allocated = sum
	}

	if allocated > in.Amount {
		return nil, "", ErrOverAllocated
	}

	request := s.receiptRequest(preview{
		CustomerID:  in.CustomerID,
		ActivityID:  in.ActivityID,
		ReceivedAt:  in.ReceivedAt,
		Notes:       in.Notes,
		Amount:      in.Amount,
		Method:      in.Method,
		Reference:   in.Reference,
		Allocations: allocations,
		OnAccount:   in.Amount - allocated,
	})

	return s.documents.PreviewReceipt(ctx, request)
}

// preview is the shape a receipt is built from, whether it is being previewed
// or actually issued.
type preview struct {
	CustomerID  string
	ActivityID  *string
	ReceivedAt  string
	Notes       string
	Amount      money.Amount
	Method      Method
	Reference   string
	Allocations []Allocation
	OnAccount   money.Amount
}

// receiptRequest turns a payment into the receipt it produces. Both the preview
// and the real issuance go through here, so what the operator sees is what gets
// issued.
func (s *Service) receiptRequest(p preview) documents.ReceiptRequest {
	snapshot := &documents.PaymentSnapshot{
		AmountAgorot:    p.Amount.Agorot(),
		ReceivedAt:      p.ReceivedAt,
		Method:          string(p.Method),
		MethodHebrew:    p.Method.HebrewName(),
		Reference:       p.Reference,
		OnAccountAgorot: p.OnAccount.Agorot(),
	}

	lines := make([]documents.LineInput, 0, len(p.Allocations)+1)
	settledIDs := make([]string, 0, len(p.Allocations))

	for _, allocation := range p.Allocations {
		documentType := documents.Type(allocation.DocumentType)
		snapshot.SettledDocuments = append(snapshot.SettledDocuments, documents.SettledDocument{
			DocumentID:   allocation.DocumentID,
			TypeHebrew:   documentType.HebrewName(),
			FullNumber:   allocation.FullNumber,
			AmountAgorot: allocation.Amount.Agorot(),
		})
		settledIDs = append(settledIDs, allocation.DocumentID)

		lines = append(lines, documents.LineInput{
			Description:   "תשלום עבור " + documentType.HebrewName() + " " + allocation.FullNumber,
			QuantityMilli: money.QuantityScale,
			UnitPrice:     allocation.Amount,
		})
	}

	if p.OnAccount > 0 {
		lines = append(lines, documents.LineInput{
			Description:   "תשלום על החשבון",
			QuantityMilli: money.QuantityScale,
			UnitPrice:     p.OnAccount,
		})
	}

	return documents.ReceiptRequest{
		CustomerID:         p.CustomerID,
		ActivityID:         p.ActivityID,
		DocumentDate:       p.ReceivedAt,
		Notes:              p.Notes,
		Lines:              lines,
		Payment:            snapshot,
		SettledDocumentIDs: settledIDs,
	}
}

// applyAllocations records what the payment settles, refusing to allocate more
// than arrived or more than a document still owes.
//
// Each target document's row is locked while its outstanding amount is checked,
// so two payments landing on the same invoice at the same moment cannot both
// believe the full balance is theirs to settle.
func (s *Service) applyAllocations(ctx context.Context, tx pgx.Tx, paymentID string, in Input) ([]AllocationInput, error) {
	var allocatedTotal money.Amount

	for _, allocation := range in.Allocations {
		total, alreadyAllocated, err := s.documents.SettleableAmount(ctx, tx, allocation.DocumentID, in.CustomerID)
		if err != nil {
			return nil, err
		}

		outstanding := total - alreadyAllocated
		if allocation.Amount > outstanding {
			return nil, fmt.Errorf("%w: document has %s outstanding, tried to allocate %s",
				ErrDocumentOverpaid, outstanding, allocation.Amount)
		}

		if err := s.repo.InsertAllocation(ctx, tx, paymentID, allocation.DocumentID, allocation.Amount); err != nil {
			return nil, err
		}

		if allocatedTotal, err = allocatedTotal.Add(allocation.Amount); err != nil {
			return nil, err
		}
	}

	// The database cannot express "the sum of these rows is at most that
	// column", so it is checked here, inside the transaction that writes them.
	if allocatedTotal > in.Amount {
		return nil, ErrOverAllocated
	}
	return in.Allocations, nil
}

// issueReceiptTx builds and issues the receipt for a payment, inside the
// caller's transaction.
func (s *Service) issueReceiptTx(ctx context.Context, tx pgx.Tx, actor *users.User, paymentID, requestID string) error {
	payment, err := s.repo.Get(ctx, tx, paymentID)
	if err != nil {
		return err
	}

	request := s.receiptRequest(preview{
		CustomerID:  payment.CustomerID,
		ActivityID:  payment.ActivityID,
		ReceivedAt:  payment.ReceivedAt,
		Notes:       payment.Notes,
		Amount:      payment.Amount,
		Method:      payment.Method,
		Reference:   payment.Reference,
		Allocations: payment.Allocations,
		OnAccount:   payment.OnAccount,
	})
	request.Payment.ID = payment.ID

	receipt, err := s.documents.IssueReceipt(ctx, tx, actor, request, requestID)
	if err != nil {
		return err
	}

	// Attaching the receipt is the last write the payment accepts; the trigger
	// freezes the row from here on.
	if err := s.repo.AttachReceipt(ctx, tx, paymentID, receipt.ID, actor.ID); err != nil {
		return err
	}

	return s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: actor.ID,
		ActorEmail:  actor.Email,
		Operation:   audit.OpReceiptIssued,
		EntityType:  audit.EntityPayment,
		EntityID:    paymentID,
		RequestID:   requestID,
		After: map[string]any{
			"receipt_document_id": receipt.ID,
			"full_number":         receipt.FullNumber,
			"total_agorot":        receipt.Total.Agorot(),
		},
	})
}

// assertCustomerUsable refuses to take a payment against an archived customer.
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
