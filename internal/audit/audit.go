// Package audit writes the append-only audit trail. Every financial and
// security action records one event inside the same transaction as the action
// itself, so an audited action cannot commit without its trail
// (plan.md 16, ARCHITECTURE.md, Audit).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Operation names. These are stable identifiers stored in the database and
// shown in the activity log, so they are added to rather than renamed.
const (
	OpLoginSucceeded    = "AUTH_LOGIN_SUCCEEDED"
	OpLoginFailed       = "AUTH_LOGIN_FAILED"
	OpLoginBlocked      = "AUTH_LOGIN_BLOCKED"
	OpLogout            = "AUTH_LOGOUT"
	OpPasswordChanged   = "AUTH_PASSWORD_CHANGED"
	OpUserCreated       = "USER_CREATED"
	OpUserRolesChanged  = "USER_ROLES_CHANGED"
	OpUserActivated     = "USER_ACTIVATED"
	OpUserDeactivated   = "USER_DEACTIVATED"
	OpSettingsUpdated   = "SETTINGS_UPDATED"
	OpRegulatoryUpdated = "REGULATORY_CONFIG_UPDATED"

	// Customer, catalogue and activity changes. These go beyond the minimum
	// list in plan.md 16: customer details are copied onto issued documents, so
	// "who changed this business number, and when" is worth being able to
	// answer even though the change cannot alter a document already issued.
	OpCustomerCreated  = "CUSTOMER_CREATED"
	OpCustomerUpdated  = "CUSTOMER_UPDATED"
	OpCustomerArchived = "CUSTOMER_ARCHIVED"
	OpCustomerRestored = "CUSTOMER_RESTORED"

	OpServiceCreated  = "SERVICE_CREATED"
	OpServiceUpdated  = "SERVICE_UPDATED"
	OpServiceArchived = "SERVICE_ARCHIVED"
	OpServiceRestored = "SERVICE_RESTORED"

	OpActivityCreated = "ACTIVITY_CREATED"
	OpActivityUpdated = "ACTIVITY_UPDATED"
	OpActivityDeleted = "ACTIVITY_DELETED"

	// Documents. Issuance and cancellation are the events plan.md 16 requires;
	// draft events are recorded too, so the trail explains where an issued
	// document came from.
	OpDocumentDraftCreated = "DOCUMENT_DRAFT_CREATED"
	OpDocumentDraftUpdated = "DOCUMENT_DRAFT_UPDATED"
	OpDocumentDraftDeleted = "DOCUMENT_DRAFT_DELETED"
	OpDocumentIssued       = "DOCUMENT_ISSUED"
	OpDocumentCancelled    = "DOCUMENT_CANCELLED"
	OpDocumentDownloaded   = "DOCUMENT_DOWNLOADED"

	// Payments and the receipts that acknowledge them (plan.md 16).
	OpPaymentRecorded = "PAYMENT_RECORDED"
	OpReceiptIssued   = "RECEIPT_ISSUED"

	// Expenses and their attachments.
	OpExpenseCreated           = "EXPENSE_CREATED"
	OpExpenseUpdated           = "EXPENSE_UPDATED"
	OpExpenseDeleted           = "EXPENSE_DELETED"
	OpExpenseAttachmentAdded   = "EXPENSE_ATTACHMENT_ADDED"
	OpExpenseAttachmentRemoved = "EXPENSE_ATTACHMENT_REMOVED"

	// Delivery and exports (plan.md 16).
	OpDeliveryQueued   = "DELIVERY_QUEUED"
	OpDeliverySent     = "DELIVERY_SENT"
	OpDeliveryFailed   = "DELIVERY_FAILED"
	OpExportRequested  = "EXPORT_REQUESTED"
	OpExportBuilt      = "EXPORT_BUILT"
	OpExportDownloaded = "EXPORT_DOWNLOADED"

	// Backups (plan.md 16, 18).
	OpBackupCompleted = "BACKUP_COMPLETED"
	OpBackupFailed    = "BACKUP_FAILED"
)

// Entity types referenced by events.
const (
	EntityUser             = "user"
	EntitySession          = "session"
	EntityBusinessProfile  = "business_profile"
	EntityRegulatoryConfig = "regulatory_config"
	EntityCustomer         = "customer"
	EntityService          = "service"
	EntityActivity         = "activity"
	EntityDocument         = "document"
	EntityPayment          = "payment"
	EntityExpense          = "expense"
	EntityAttachment       = "attachment"
	EntityExport           = "export"
	EntityBackup           = "backup"
)

// Event is one audit record (plan.md 16 core fields).
type Event struct {
	ID            int64           `json:"id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	ActorUserID   *string         `json:"actor_user_id"`
	ActorEmail    *string         `json:"actor_email"`
	Operation     string          `json:"operation"`
	EntityType    *string         `json:"entity_type"`
	EntityID      *string         `json:"entity_id"`
	RequestID     *string         `json:"request_id"`
	BeforeSummary json.RawMessage `json:"before_summary,omitempty"`
	AfterSummary  json.RawMessage `json:"after_summary,omitempty"`
	Reason        *string         `json:"reason"`
	IP            *string         `json:"ip"`
}

// Entry describes an event to record.
type Entry struct {
	ActorUserID string
	ActorEmail  string
	Operation   string
	EntityType  string
	EntityID    string
	RequestID   string
	Before      any
	After       any
	Reason      string
	IP          string
}

// Recorder appends events.
type Recorder struct{}

// NewRecorder returns a stateless recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Record appends one event using the caller's Querier, which is normally the
// transaction performing the audited action.
func (rec *Recorder) Record(ctx context.Context, q db.Querier, e Entry) error {
	before, err := marshalSummary(e.Before)
	if err != nil {
		return fmt.Errorf("marshal before_summary: %w", err)
	}
	after, err := marshalSummary(e.After)
	if err != nil {
		return fmt.Errorf("marshal after_summary: %w", err)
	}

	_, err = q.Exec(ctx, `
		INSERT INTO audit_events
			(actor_user_id, actor_email, operation, entity_type, entity_id,
			 request_id, before_summary, after_summary, reason, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		nullable(e.ActorUserID), nullable(e.ActorEmail), e.Operation,
		nullable(e.EntityType), nullable(e.EntityID), nullable(e.RequestID),
		before, after, nullable(e.Reason), nullable(e.IP))
	if err != nil {
		return fmt.Errorf("record audit event %q: %w", e.Operation, err)
	}
	return nil
}

// ListParams filters the audit log.
type ListParams struct {
	Limit      int
	Before     *time.Time
	Operation  string
	EntityType string
	EntityID   string
	ActorID    string
}

// List returns events newest first. The log is read-only everywhere.
func (rec *Recorder) List(ctx context.Context, q db.Querier, p ListParams) ([]Event, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}

	rows, err := q.Query(ctx, `
		SELECT id, occurred_at, actor_user_id, actor_email, operation,
		       entity_type, entity_id, request_id, before_summary, after_summary,
		       reason, host(ip)
		FROM audit_events
		WHERE ($1::timestamptz IS NULL OR occurred_at < $1)
		  AND ($2::text IS NULL OR operation = $2)
		  AND ($3::text IS NULL OR entity_type = $3)
		  AND ($4::text IS NULL OR entity_id = $4)
		  AND ($5::uuid IS NULL OR actor_user_id = $5)
		ORDER BY occurred_at DESC, id DESC
		LIMIT $6`,
		p.Before, nullable(p.Operation), nullable(p.EntityType),
		nullable(p.EntityID), nullable(p.ActorID), p.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]Event, 0, p.Limit)
	for rows.Next() {
		var e Event
		if err := rows.Scan(
			&e.ID, &e.OccurredAt, &e.ActorUserID, &e.ActorEmail, &e.Operation,
			&e.EntityType, &e.EntityID, &e.RequestID, &e.BeforeSummary,
			&e.AfterSummary, &e.Reason, &e.IP,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func marshalSummary(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
