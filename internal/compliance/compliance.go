// Package compliance isolates Israeli tax rules from domain logic. Every
// question the compliance gate has not resolved is answered here — as a refusal
// — rather than being guessed inside documents, payments or reports
// (plan.md 2, 22.3, 22.4).
//
// Nothing in this package invents a legal rule. It reads effective-dated
// configuration and reports what the configuration permits.
package compliance

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
)

// Document types (plan.md 9). TAX_INVOICE is intentionally not a constant here:
// exempt-dealer mode must never issue one, and the type does not exist in v1.
const (
	DocTransactionInvoice = "TRANSACTION_INVOICE" // חשבונית עסקה
	DocPaymentRequest     = "PAYMENT_REQUEST"     // דרישת תשלום
	DocReceipt            = "RECEIPT"             // קבלה
)

// Decision is the outcome of a compliance check. Reason is Hebrew, because it
// is shown to the user when an action is blocked.
type Decision struct {
	Allowed bool
	Reason  string
}

// Allow is a permitted decision.
func Allow() Decision { return Decision{Allowed: true} }

// Block is a refusal carrying a Hebrew explanation.
func Block(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

// Rules is the resolved rule set for one business date.
type Rules struct {
	BusinessDate    time.Time
	Mode            settings.BusinessMode
	AllowedDocTypes []string
	Gate            settings.ComplianceGate
	Threshold       settings.Threshold
	ThresholdKnown  bool
}

// Service resolves rules from configuration.
type Service struct {
	settings *settings.Service
	location *time.Location
}

// NewService wires the compliance adapter to the settings source and the
// business timezone.
func NewService(s *settings.Service, location *time.Location) *Service {
	return &Service{settings: s, location: location}
}

// Today returns the current business date in the business timezone.
func (s *Service) Today() time.Time {
	now := time.Now().In(s.location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.location)
}

// RulesFor resolves the rule set effective on businessDate.
func (s *Service) RulesFor(ctx context.Context, businessDate time.Time) (*Rules, error) {
	mode, err := s.settings.BusinessMode(ctx, businessDate)
	if err != nil {
		return nil, fmt.Errorf("resolve business mode: %w", err)
	}
	allowed, err := s.settings.AllowedDocumentTypes(ctx, businessDate)
	if err != nil {
		return nil, fmt.Errorf("resolve allowed document types: %w", err)
	}
	gate, err := s.settings.ComplianceGate(ctx, businessDate)
	if err != nil {
		return nil, fmt.Errorf("resolve compliance gate: %w", err)
	}

	rules := &Rules{
		BusinessDate:    businessDate,
		Mode:            mode,
		AllowedDocTypes: allowed,
		Gate:            gate,
	}

	// A missing threshold is not an error for the caller: turnover progress is
	// simply unknown for that year until the value is configured.
	if threshold, err := s.settings.ExemptTurnoverThreshold(ctx, businessDate); err == nil {
		rules.Threshold = threshold
		rules.ThresholdKnown = true
	}

	return rules, nil
}

// CurrentRules resolves the rules for today.
func (s *Service) CurrentRules(ctx context.Context) (*Rules, error) {
	return s.RulesFor(ctx, s.Today())
}

// VATApplies reports whether VAT may be added to a sale. Exempt dealers never
// charge VAT (plan.md 2).
func (r *Rules) VATApplies() bool {
	return r.Mode == settings.ModeAuthorizedDealer
}

// CanIssueDocumentType reports whether a document type may be issued at all in
// the current mode.
func (r *Rules) CanIssueDocumentType(docType string) Decision {
	if r.Mode == settings.ModeExemptDealer && docType == "TAX_INVOICE" {
		return Block("עוסק פטור אינו רשאי להפיק חשבונית מס.")
	}
	if !slices.Contains(r.AllowedDocTypes, docType) {
		return Block("סוג המסמך אינו מורשה בהגדרות העסק.")
	}
	return Allow()
}

// CanIssueForReal reports whether official documents may be issued for real.
// It stays blocked until a tax adviser resolves every gate item and enables
// issuance in configuration (plan.md 2, 20 Phase 7).
func (r *Rules) CanIssueForReal() Decision {
	if !r.Gate.RealIssuanceEnabled {
		return Block("הפקת מסמכים רשמיים חסומה עד לאישור רואה חשבון והשלמת בדיקת ההתאמה הרגולטורית.")
	}
	if open := r.Gate.Unresolved(); len(open) > 0 {
		return Block("בדיקת ההתאמה הרגולטורית טרם הושלמה.")
	}
	return Allow()
}

// RequiresExemptDealerWording reports whether documents must display עוסק פטור.
func (r *Rules) RequiresExemptDealerWording() bool {
	return r.Mode == settings.ModeExemptDealer
}

// Status is the compliance summary the UI shows, so the operator always knows
// whether the system is in test or live mode.
type Status struct {
	BusinessDate        string   `json:"business_date"`
	Mode                string   `json:"mode"`
	ModeHebrew          string   `json:"mode_hebrew"`
	VATApplies          bool     `json:"vat_applies"`
	AllowedDocTypes     []string `json:"allowed_document_types"`
	RealIssuanceEnabled bool     `json:"real_issuance_enabled"`
	UnresolvedGateItems []string `json:"unresolved_gate_items"`
	ThresholdKnown      bool     `json:"threshold_known"`
	ThresholdAgorot     int64    `json:"threshold_agorot"`
}

// Status renders the rule set for the API.
func (r *Rules) Status() Status {
	open := r.Gate.Unresolved()
	slices.Sort(open)
	if open == nil {
		open = []string{}
	}

	modeHebrew := "עוסק פטור"
	if r.Mode == settings.ModeAuthorizedDealer {
		modeHebrew = "עוסק מורשה"
	}

	return Status{
		BusinessDate:        r.BusinessDate.Format("2006-01-02"),
		Mode:                string(r.Mode),
		ModeHebrew:          modeHebrew,
		VATApplies:          r.VATApplies(),
		AllowedDocTypes:     r.AllowedDocTypes,
		RealIssuanceEnabled: r.Gate.RealIssuanceEnabled,
		UnresolvedGateItems: open,
		ThresholdKnown:      r.ThresholdKnown,
		ThresholdAgorot:     r.Threshold.AmountAgorot,
	}
}
