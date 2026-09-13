package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/jobs"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Channel is how a document reaches a customer.
type Channel string

const (
	// ChannelEmail sends the PDF as an attachment.
	ChannelEmail Channel = "EMAIL"
	// ChannelWhatsAppLink prepares a message the operator sends themselves.
	// The Business API is a future step (plan.md 14).
	ChannelWhatsAppLink Channel = "WHATSAPP_LINK"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool { return c == ChannelEmail || c == ChannelWhatsAppLink }

// Attempt states.
const (
	StateQueued = "QUEUED"
	StateSent   = "SENT"
	StateFailed = "FAILED"
)

// Sentinel errors the HTTP layer maps to the API error catalogue.
var (
	ErrNotIssued      = errors.New("only an issued document can be sent")
	ErrNoEmailAddress = errors.New("the customer has no email address")
	ErrNoPhoneNumber  = errors.New("the customer has no phone number")
)

// Attempt is one recorded try at delivering a document.
type Attempt struct {
	ID          string    `json:"id"`
	DocumentID  string    `json:"document_id"`
	Channel     Channel   `json:"channel"`
	Recipient   string    `json:"recipient"`
	State       string    `json:"state"`
	Error       *string   `json:"error"`
	AttemptedAt time.Time `json:"attempted_at"`
}

// sendPayload is what a queued send job carries.
type sendPayload struct {
	DocumentID string `json:"document_id"`
	Recipient  string `json:"recipient"`
	ActorID    string `json:"actor_id"`
	ActorEmail string `json:"actor_email"`
}

// Service queues and performs deliveries.
type Service struct {
	pool      *db.Pool
	documents *documents.Service
	settings  *settings.Service
	mailer    Mailer
	queue     *jobs.Queue
	audit     *audit.Recorder
	location  *time.Location
}

// NewService wires the delivery service.
func NewService(
	pool *db.Pool,
	documentService *documents.Service,
	settingsService *settings.Service,
	mailer Mailer,
	queue *jobs.Queue,
	recorder *audit.Recorder,
	location *time.Location,
) *Service {
	return &Service{
		pool: pool, documents: documentService, settings: settingsService,
		mailer: mailer, queue: queue, audit: recorder, location: location,
	}
}

// EmailAvailable reports whether email can be sent at all.
func (s *Service) EmailAvailable() bool { return s.mailer.Configured() }

// QueueEmail schedules an email for an issued document.
//
// It only ever *queues*. The document is already issued and committed by the
// time this runs, so nothing here can undo it — which is exactly the guarantee
// plan.md 14 asks for.
func (s *Service) QueueEmail(ctx context.Context, actor *users.User, documentID, recipient, requestID string) (*Attempt, error) {
	document, err := s.documents.Get(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if document.State == documents.StateDraft {
		return nil, ErrNotIssued
	}

	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		recipient, err = s.customerEmail(ctx, document.CustomerID)
		if err != nil {
			return nil, err
		}
	}
	if recipient == "" {
		return nil, ErrNoEmailAddress
	}

	var attempt *Attempt
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		jobID, err := s.queue.Enqueue(ctx, tx, jobs.EnqueueParams{
			Kind: jobs.KindSendDocument,
			Payload: sendPayload{
				DocumentID: documentID,
				Recipient:  recipient,
				ActorID:    actor.ID,
				ActorEmail: actor.Email,
			},
			MaxAttempts: 5,
			ActorID:     actor.ID,
		})
		if err != nil {
			return err
		}

		if attempt, err = s.recordAttempt(ctx, tx, attemptRecord{
			DocumentID: documentID,
			Channel:    ChannelEmail,
			Recipient:  recipient,
			State:      StateQueued,
			JobID:      &jobID,
			ActorID:    actor.ID,
		}); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDeliveryQueued,
			EntityType:  audit.EntityDocument,
			EntityID:    documentID,
			RequestID:   requestID,
			After:       map[string]any{"channel": ChannelEmail, "recipient": recipient, "job_id": jobID},
		})
	})
	if err != nil {
		return nil, err
	}
	return attempt, nil
}

// WhatsAppShare is a prepared message for the operator to send themselves.
type WhatsAppShare struct {
	Phone       string `json:"phone"`
	Message     string `json:"message"`
	ShareURL    string `json:"share_url"`
	DownloadURL string `json:"download_url"`
}

// PrepareWhatsApp builds the deep link and Hebrew message for a document.
//
// v1 is deliberately user-initiated: the operator taps the link and sends from
// their own WhatsApp. The Business API, with its templates and approvals, is a
// later step (plan.md 14).
func (s *Service) PrepareWhatsApp(ctx context.Context, actor *users.User, documentID, requestID string) (*WhatsAppShare, error) {
	document, err := s.documents.Get(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if document.State == documents.StateDraft {
		return nil, ErrNotIssued
	}

	phone, err := s.customerPhone(ctx, document.CustomerID)
	if err != nil {
		return nil, err
	}
	if phone == "" {
		return nil, ErrNoPhoneNumber
	}

	profile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, err
	}

	businessName := profile.DisplayName
	if strings.TrimSpace(businessName) == "" {
		businessName = profile.LegalName
	}

	message := fmt.Sprintf("שלום %s,\nמצורף %s %s מאת %s.\nסכום: %s ₪.\nתודה רבה!",
		document.CustomerName,
		document.DocumentType.HebrewName(),
		document.FullNumber,
		businessName,
		formatAmount(document.Total.Agorot()))

	share := &WhatsAppShare{
		Phone:   phone,
		Message: message,
		// wa.me opens WhatsApp on mobile and WhatsApp Web on desktop.
		ShareURL:    "https://wa.me/" + internationalize(phone) + "?text=" + url.QueryEscape(message),
		DownloadURL: "/api/v1/documents/" + documentID + "/pdf",
	}

	// Recorded like any other delivery: the operator can see what was prepared
	// and when, even though the send itself happens in their own app.
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.recordAttempt(ctx, tx, attemptRecord{
			DocumentID: documentID,
			Channel:    ChannelWhatsAppLink,
			Recipient:  phone,
			State:      StateSent,
			ActorID:    actor.ID,
		}); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpDeliverySent,
			EntityType:  audit.EntityDocument,
			EntityID:    documentID,
			RequestID:   requestID,
			After:       map[string]any{"channel": ChannelWhatsAppLink, "recipient": phone},
			Reason:      "prepared a share link; the operator sends it from their own WhatsApp",
		})
	}); err != nil {
		return nil, err
	}

	return share, nil
}

// HandleSendJob performs a queued email send. It is registered with the worker.
//
// Returning an error schedules a retry with backoff, which is how a temporary
// mail-server failure resolves itself (plan.md 14).
func (s *Service) HandleSendJob(ctx context.Context, job *jobs.Job) (any, error) {
	var payload sendPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode send payload: %w", err)
	}

	document, err := s.documents.Get(ctx, payload.DocumentID)
	if err != nil {
		return nil, err
	}

	pdfBytes, filename, err := s.documents.PDF(ctx, payload.DocumentID)
	if err != nil {
		return nil, err
	}

	profile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, err
	}
	businessName := profile.DisplayName
	if strings.TrimSpace(businessName) == "" {
		businessName = profile.LegalName
	}

	subject := fmt.Sprintf("%s %s מאת %s",
		document.DocumentType.HebrewName(), document.FullNumber, businessName)

	body := fmt.Sprintf(
		"שלום %s,\n\nמצורף %s %s בסך %s ₪.\n\nבברכה,\n%s\n%s",
		document.CustomerName,
		document.DocumentType.HebrewName(),
		document.FullNumber,
		formatAmount(document.Total.Agorot()),
		businessName,
		strings.TrimSpace(profile.Phone))

	sendErr := s.mailer.Send(ctx, Message{
		To:         payload.Recipient,
		Subject:    subject,
		Body:       body,
		Attachment: pdfBytes,
		AttachName: filename,
		AttachMIME: "application/pdf",
	})

	// The attempt is recorded either way: a failure the operator cannot see is
	// worse than one they can.
	state, operation := StateSent, audit.OpDeliverySent
	var errorText *string
	if sendErr != nil {
		message := sendErr.Error()
		state, operation, errorText = StateFailed, audit.OpDeliveryFailed, &message
	}

	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.recordAttempt(ctx, tx, attemptRecord{
			DocumentID: payload.DocumentID,
			Channel:    ChannelEmail,
			Recipient:  payload.Recipient,
			State:      state,
			Error:      errorText,
			JobID:      &job.ID,
			ActorID:    payload.ActorID,
		}); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: payload.ActorID,
			ActorEmail:  payload.ActorEmail,
			Operation:   operation,
			EntityType:  audit.EntityDocument,
			EntityID:    payload.DocumentID,
			After:       map[string]any{"channel": ChannelEmail, "recipient": payload.Recipient},
			Reason:      derefOrEmpty(errorText),
		})
	}); err != nil {
		return nil, err
	}

	if sendErr != nil {
		return nil, sendErr
	}
	return map[string]any{"recipient": payload.Recipient, "channel": ChannelEmail}, nil
}

// Attempts returns a document's delivery history, newest first.
func (s *Service) Attempts(ctx context.Context, documentID string) ([]Attempt, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, document_id::text, channel, recipient, state, error, attempted_at
		FROM delivery_attempts
		WHERE document_id = $1
		ORDER BY attempted_at DESC`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attempts := []Attempt{}
	for rows.Next() {
		var attempt Attempt
		if err := rows.Scan(&attempt.ID, &attempt.DocumentID, &attempt.Channel,
			&attempt.Recipient, &attempt.State, &attempt.Error, &attempt.AttemptedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// RecentFailures lists failed deliveries for the dashboard (plan.md 7).
func (s *Service) RecentFailures(ctx context.Context, limit int) ([]Attempt, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id::text, document_id::text, channel, recipient, state, error, attempted_at
		FROM delivery_attempts
		WHERE state = 'FAILED'
		ORDER BY attempted_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attempts := []Attempt{}
	for rows.Next() {
		var attempt Attempt
		if err := rows.Scan(&attempt.ID, &attempt.DocumentID, &attempt.Channel,
			&attempt.Recipient, &attempt.State, &attempt.Error, &attempt.AttemptedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// attemptRecord is one row to write to the delivery history.
type attemptRecord struct {
	DocumentID string
	Channel    Channel
	Recipient  string
	State      string
	Error      *string
	JobID      *string
	ActorID    string
}

func (s *Service) recordAttempt(ctx context.Context, q db.Querier, r attemptRecord) (*Attempt, error) {
	var (
		attempt  Attempt
		jobArg   any
		actorArg any
	)
	if r.JobID != nil && *r.JobID != "" {
		jobArg = *r.JobID
	}
	if r.ActorID != "" {
		actorArg = r.ActorID
	}

	err := q.QueryRow(ctx, `
		INSERT INTO delivery_attempts
			(document_id, channel, recipient, state, error, job_id, attempted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, document_id::text, channel, recipient, state, error, attempted_at`,
		r.DocumentID, string(r.Channel), r.Recipient, r.State, r.Error, jobArg, actorArg).
		Scan(&attempt.ID, &attempt.DocumentID, &attempt.Channel, &attempt.Recipient,
			&attempt.State, &attempt.Error, &attempt.AttemptedAt)
	if err != nil {
		return nil, fmt.Errorf("record delivery attempt: %w", err)
	}
	return &attempt, nil
}

func (s *Service) customerEmail(ctx context.Context, customerID string) (string, error) {
	var email string
	err := s.pool.QueryRow(ctx, `SELECT email FROM customers WHERE id = $1`, customerID).Scan(&email)
	return strings.TrimSpace(email), err
}

func (s *Service) customerPhone(ctx context.Context, customerID string) (string, error) {
	var phone string
	err := s.pool.QueryRow(ctx, `SELECT phone FROM customers WHERE id = $1`, customerID).Scan(&phone)
	return strings.TrimSpace(phone), err
}

// internationalize turns an Israeli local number into the digits wa.me expects:
// 050-123-4567 becomes 972501234567.
func internationalize(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)

	switch {
	case strings.HasPrefix(digits, "972"):
		return digits
	case strings.HasPrefix(digits, "0"):
		return "972" + digits[1:]
	default:
		return digits
	}
}

// formatAmount renders agorot as a plain decimal for message text.
func formatAmount(agorot int64) string {
	negative := agorot < 0
	if negative {
		agorot = -agorot
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, agorot/100, agorot%100)
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
