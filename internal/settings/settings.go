// Package settings owns the effective-dated regulatory configuration and the
// mutable business profile. No regulatory value is a Go constant: everything is
// data resolved for a business date (plan.md 3.7, ARCHITECTURE.md, Regulatory configuration).
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Configuration keys. Adding a key is a migration plus a typed accessor here.
const (
	KeyBusinessMode                 = "business_mode"
	KeyExemptTurnoverThreshold      = "exempt_turnover_threshold"
	KeyAllowedDocumentTypes         = "allowed_document_types"
	KeyComplianceGate               = "compliance_gate"
	KeyComputerizedDocumentSettings = "computerized_document_settings"
)

// ErrNotConfigured means no row is effective for the requested date. Callers
// must treat this as "unknown", never as a default (plan.md 22.3).
var ErrNotConfigured = errors.New("no regulatory value effective for that date")

// BusinessMode is the tax status the business operates under.
type BusinessMode string

const (
	// ModeExemptDealer is עוסק פטור: no VAT, no tax invoices.
	ModeExemptDealer BusinessMode = "EXEMPT_DEALER"
	// ModeAuthorizedDealer is עוסק מורשה. Architected for, disabled in v1.
	ModeAuthorizedDealer BusinessMode = "AUTHORIZED_DEALER"
)

// BusinessProfile is the business's own identity, snapshotted onto every
// issued document at issuance time (plan.md 11).
type BusinessProfile struct {
	LegalName      string    `json:"legal_name"`
	DisplayName    string    `json:"display_name"`
	BusinessNumber string    `json:"business_number"`
	Address        string    `json:"address"`
	Phone          string    `json:"phone"`
	Email          string    `json:"email"`
	Website        string    `json:"website"`
	BankDetails    string    `json:"bank_details"`
	FooterNote     string    `json:"footer_note"`
	UpdatedAt      time.Time `json:"updated_at"`

	// Logo metadata. The bytes live in the file store; these fields say where
	// and let a client know whether there is one to show.
	LogoPath        string     `json:"-"`
	LogoContentType string     `json:"logo_content_type,omitempty"`
	LogoSHA256      string     `json:"logo_sha256,omitempty"`
	LogoByteSize    int64      `json:"logo_byte_size,omitempty"`
	LogoUploadedAt  *time.Time `json:"logo_uploaded_at,omitempty"`
	HasLogo         bool       `json:"has_logo"`
}

// RegulatoryEntry is one effective-dated configuration row.
type RegulatoryEntry struct {
	Key           string          `json:"key"`
	EffectiveFrom string          `json:"effective_from"`
	Value         json.RawMessage `json:"value"`
	Note          *string         `json:"note"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Threshold is the exempt-dealer annual turnover ceiling, in agorot.
type Threshold struct {
	AmountAgorot int64  `json:"amount_agorot"`
	Currency     string `json:"currency"`
}

// ComplianceGate records which legal questions a tax adviser has resolved.
// Real issuance stays blocked until every item is cleared (plan.md 2).
type ComplianceGate struct {
	RealIssuanceEnabled bool              `json:"real_issuance_enabled"`
	ClearedBy           *string           `json:"cleared_by"`
	ClearedAt           *string           `json:"cleared_at"`
	Items               map[string]string `json:"items"`
}

// Unresolved lists the gate items still open.
func (g ComplianceGate) Unresolved() []string {
	var open []string
	for item, status := range g.Items {
		if !strings.EqualFold(status, "RESOLVED") {
			open = append(open, item)
		}
	}
	return open
}

// Service reads and writes settings.
type Service struct {
	pool  *db.Pool
	audit *audit.Recorder
	store *storage.Store
}

// NewService wires the settings service.
func NewService(pool *db.Pool, recorder *audit.Recorder, store *storage.Store) *Service {
	return &Service{pool: pool, audit: recorder, store: store}
}

// ---------------------------------------------------------- regulatory ----

// RawValue returns the JSON value effective on businessDate: the row with the
// greatest effective_from that is not after that date.
func (s *Service) RawValue(ctx context.Context, q users.Querier, key string, businessDate time.Time) (json.RawMessage, error) {
	var raw json.RawMessage
	err := q.QueryRow(ctx, `
		SELECT value
		FROM regulatory_config
		WHERE key = $1 AND effective_from <= $2::date
		ORDER BY effective_from DESC
		LIMIT 1`, key, businessDate.Format("2006-01-02")).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: key %q on %s", ErrNotConfigured, key, businessDate.Format("2006-01-02"))
		}
		return nil, err
	}
	return raw, nil
}

// value decodes the effective value of key into dst.
func (s *Service) value(ctx context.Context, key string, businessDate time.Time, dst any) error {
	raw, err := s.RawValue(ctx, s.pool, key, businessDate)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode regulatory value %q: %w", key, err)
	}
	return nil
}

// BusinessMode returns the mode effective on businessDate.
func (s *Service) BusinessMode(ctx context.Context, businessDate time.Time) (BusinessMode, error) {
	var mode BusinessMode
	if err := s.value(ctx, KeyBusinessMode, businessDate, &mode); err != nil {
		return "", err
	}
	return mode, nil
}

// ExemptTurnoverThreshold returns the annual ceiling effective on businessDate.
// A missing year is an error, never a guessed number.
func (s *Service) ExemptTurnoverThreshold(ctx context.Context, businessDate time.Time) (Threshold, error) {
	var t Threshold
	if err := s.value(ctx, KeyExemptTurnoverThreshold, businessDate, &t); err != nil {
		return Threshold{}, err
	}
	return t, nil
}

// AllowedDocumentTypes returns the document types this deployment may issue.
func (s *Service) AllowedDocumentTypes(ctx context.Context, businessDate time.Time) ([]string, error) {
	var types []string
	if err := s.value(ctx, KeyAllowedDocumentTypes, businessDate, &types); err != nil {
		return nil, err
	}
	return types, nil
}

// ComplianceGate returns the gate state effective on businessDate.
func (s *Service) ComplianceGate(ctx context.Context, businessDate time.Time) (ComplianceGate, error) {
	var gate ComplianceGate
	if err := s.value(ctx, KeyComplianceGate, businessDate, &gate); err != nil {
		return ComplianceGate{}, err
	}
	return gate, nil
}

// ListRegulatory returns the full history of a key, or of every key when key
// is empty, newest first.
func (s *Service) ListRegulatory(ctx context.Context, key string) ([]RegulatoryEntry, error) {
	var keyArg any
	if key != "" {
		keyArg = key
	}

	rows, err := s.pool.Query(ctx, `
		SELECT key, effective_from, value, note, created_at
		FROM regulatory_config
		WHERE ($1::text IS NULL OR key = $1)
		ORDER BY key, effective_from DESC`, keyArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]RegulatoryEntry, 0, 16)
	for rows.Next() {
		var e RegulatoryEntry
		var effectiveFrom time.Time
		if err := rows.Scan(&e.Key, &effectiveFrom, &e.Value, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.EffectiveFrom = effectiveFrom.Format("2006-01-02")
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SetRegulatoryValue adds a new effective-dated row. Existing rows are never
// rewritten — the database blocks that — so regulatory history stays intact.
func (s *Service) SetRegulatoryValue(ctx context.Context, actor *users.User, key string, effectiveFrom time.Time, value json.RawMessage, note, requestID string) error {
	if !json.Valid(value) {
		return fmt.Errorf("value is not valid JSON")
	}

	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var previous json.RawMessage
		// Not found is fine: this may be the first row for the key.
		_ = tx.QueryRow(ctx, `
			SELECT value FROM regulatory_config
			WHERE key = $1 AND effective_from <= $2::date
			ORDER BY effective_from DESC LIMIT 1`,
			key, effectiveFrom.Format("2006-01-02")).Scan(&previous)

		if _, err := tx.Exec(ctx, `
			INSERT INTO regulatory_config (key, effective_from, value, note, created_by)
			VALUES ($1, $2::date, $3, $4, $5)`,
			key, effectiveFrom.Format("2006-01-02"), value, nullable(note), actor.ID); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpRegulatoryUpdated,
			EntityType:  audit.EntityRegulatoryConfig,
			EntityID:    key,
			RequestID:   requestID,
			Before:      json.RawMessage(previous),
			After:       map[string]any{"effective_from": effectiveFrom.Format("2006-01-02"), "value": value},
			Reason:      note,
		})
	})
}

// ------------------------------------------------------ business profile ----

// BusinessProfile returns the single profile row.
func (s *Service) BusinessProfile(ctx context.Context) (BusinessProfile, error) {
	return s.businessProfile(ctx, s.pool)
}

// businessProfile reads the single profile row through any querier.
func (s *Service) businessProfile(ctx context.Context, q users.Querier) (BusinessProfile, error) {
	var (
		p          BusinessProfile
		path       *string
		mediaType  *string
		sha        *string
		size       *int64
		uploadedAt *time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT legal_name, display_name, business_number, address, phone,
		       email, website, bank_details, footer_note, updated_at,
		       logo_path, logo_content_type, logo_sha256, logo_byte_size, logo_uploaded_at
		FROM business_profile WHERE id = true`).
		Scan(&p.LegalName, &p.DisplayName, &p.BusinessNumber, &p.Address, &p.Phone,
			&p.Email, &p.Website, &p.BankDetails, &p.FooterNote, &p.UpdatedAt,
			&path, &mediaType, &sha, &size, &uploadedAt)
	if err != nil {
		return BusinessProfile{}, err
	}

	if path != nil && sha != nil {
		p.LogoPath, p.LogoSHA256, p.HasLogo = *path, *sha, true
		if mediaType != nil {
			p.LogoContentType = *mediaType
		}
		if size != nil {
			p.LogoByteSize = *size
		}
		p.LogoUploadedAt = uploadedAt
	}
	return p, nil
}

// UpdateBusinessProfile replaces the profile and audits the change with both
// the previous and the new values.
func (s *Service) UpdateBusinessProfile(ctx context.Context, actor *users.User, p BusinessProfile, requestID string) (BusinessProfile, error) {
	var updated BusinessProfile

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.businessProfile(ctx, tx)
		if err != nil {
			return err
		}
		// Lock the row so a concurrent logo upload and profile edit serialize.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM business_profile WHERE id = true FOR UPDATE`); err != nil {
			return err
		}

		// The logo columns are deliberately absent: the logo is changed through
		// its own endpoint, so saving the form never clears it.
		if _, err := tx.Exec(ctx, `
			UPDATE business_profile
			SET legal_name = $1, display_name = $2, business_number = $3, address = $4,
			    phone = $5, email = $6, website = $7, bank_details = $8,
			    footer_note = $9, updated_by = $10
			WHERE id = true`,
			strings.TrimSpace(p.LegalName), strings.TrimSpace(p.DisplayName),
			strings.TrimSpace(p.BusinessNumber), strings.TrimSpace(p.Address),
			strings.TrimSpace(p.Phone), strings.TrimSpace(p.Email),
			strings.TrimSpace(p.Website), strings.TrimSpace(p.BankDetails),
			strings.TrimSpace(p.FooterNote), actor.ID); err != nil {
			return err
		}

		if updated, err = s.businessProfile(ctx, tx); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpSettingsUpdated,
			EntityType:  audit.EntityBusinessProfile,
			EntityID:    "business_profile",
			RequestID:   requestID,
			Before:      before,
			After:       updated,
		})
	})
	return updated, err
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
