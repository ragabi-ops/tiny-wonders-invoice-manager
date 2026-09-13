package documents

import (
	"context"
	"fmt"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/compliance"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
	tmpl "github.com/ragabix/tiny-wonders-invoice-manager/templates"
)

// previewNumber stands in for the official number a preview does not have.
const previewNumber = "טיוטה"

// Preview renders a draft as it will look once issued, without issuing it.
//
// Nothing is allocated, nothing is stored, nothing is recorded: no sequence
// number is consumed, no snapshot is written, no PDF is kept. It exists because
// issuance is irreversible — checking the document while it can still be fixed
// is the last cheap moment (plan.md 3.1, 9).
//
// The output carries a "טיוטה" watermark so a printed preview can never be
// mistaken for the document itself.
func (s *Service) Preview(ctx context.Context, id string) ([]byte, string, error) {
	document, err := s.repo.GetWithLines(ctx, s.pool, id)
	if err != nil {
		return nil, "", err
	}

	// An issued document has a real PDF; serving a watermarked re-render of it
	// would be confusing and slower.
	if document.State != StateDraft {
		return nil, "", ErrNotDraft
	}
	if len(document.Lines) == 0 {
		return nil, "", ErrNoLines
	}

	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, "", err
	}
	if decision := rules.CanIssueDocumentType(string(document.DocumentType)); !decision.Allowed {
		return nil, "", &ErrComplianceBlocked{Reason: decision.Reason}
	}

	businessProfile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read business profile: %w", err)
	}

	inputs := make([]LineInput, 0, len(document.Lines))
	for _, line := range document.Lines {
		inputs = append(inputs, LineInput{
			ServiceID:     line.ServiceID,
			Description:   line.Description,
			Unit:          line.Unit,
			QuantityMilli: line.QuantityMilli,
			UnitPrice:     line.UnitPrice,
		})
	}

	totals, err := ComputeTotals(inputs, rules.VATApplies(), 0)
	if err != nil {
		return nil, "", err
	}

	snapshot, err := s.previewSnapshot(ctx, document, totals, businessProfile, rules)
	if err != nil {
		return nil, "", err
	}

	return s.renderPreview(ctx, snapshot)
}

// PreviewReceipt renders the receipt a payment would produce, before the
// payment is recorded.
//
// Receipts are never drafts — a payment and its receipt commit together — so
// this is the only chance to look at one before it becomes immutable. It takes
// the request as the operator has filled it in and stores nothing.
func (s *Service) PreviewReceipt(ctx context.Context, req ReceiptRequest) ([]byte, string, error) {
	if len(req.Lines) == 0 {
		return nil, "", ErrNoLines
	}

	rules, err := s.compliance.CurrentRules(ctx)
	if err != nil {
		return nil, "", err
	}
	if decision := rules.CanIssueDocumentType(string(TypeReceipt)); !decision.Allowed {
		return nil, "", &ErrComplianceBlocked{Reason: decision.Reason}
	}

	businessProfile, err := s.settings.BusinessProfile(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read business profile: %w", err)
	}

	totals, err := ComputeTotals(req.Lines, false, 0)
	if err != nil {
		return nil, "", err
	}

	document := &Document{
		DocumentType: TypeReceipt,
		CustomerID:   req.CustomerID,
		ActivityID:   req.ActivityID,
		DocumentDate: req.DocumentDate,
		Notes:        req.Notes,
	}

	snapshot, err := s.previewSnapshot(ctx, document, totals, businessProfile, rules)
	if err != nil {
		return nil, "", err
	}
	snapshot.Payment = req.Payment

	return s.renderPreview(ctx, snapshot)
}

// previewSnapshot builds a snapshot-shaped value for rendering only.
//
// It is never persisted and carries no official number, because a preview has
// none. Everything else is resolved exactly as issuance would resolve it, so
// what the operator sees is what they will get.
func (s *Service) previewSnapshot(
	ctx context.Context,
	document *Document,
	totals *Totals,
	business settings.BusinessProfile,
	rules *compliance.Rules,
) (*Snapshot, error) {
	snapshot := &Snapshot{SchemaVersion: snapshotSchemaVersion}

	snapshot.Business.LegalName = business.LegalName
	snapshot.Business.DisplayName = business.DisplayName
	snapshot.Business.BusinessNumber = business.BusinessNumber
	snapshot.Business.Address = business.Address
	snapshot.Business.Phone = business.Phone
	snapshot.Business.Email = business.Email
	snapshot.Business.Website = business.Website
	snapshot.Business.BankDetails = business.BankDetails
	snapshot.Business.FooterNote = business.FooterNote
	snapshot.Business.LogoPath = business.LogoPath
	snapshot.Business.LogoSHA256 = business.LogoSHA256

	if err := s.pool.QueryRow(ctx, `
		SELECT id::text, customer_type, display_name, legal_name,
		       business_or_id_number, phone, email, address
		FROM customers WHERE id = $1`, document.CustomerID).
		Scan(&snapshot.Customer.ID, &snapshot.Customer.CustomerType,
			&snapshot.Customer.DisplayName, &snapshot.Customer.LegalName,
			&snapshot.Customer.BusinessOrIDNumber, &snapshot.Customer.Phone,
			&snapshot.Customer.Email, &snapshot.Customer.Address); err != nil {
		return nil, fmt.Errorf("read customer for preview: %w", err)
	}

	if document.ActivityID != nil && *document.ActivityID != "" {
		var activity struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := s.pool.QueryRow(ctx, `SELECT id::text, name FROM activities WHERE id = $1`,
			*document.ActivityID).Scan(&activity.ID, &activity.Name); err != nil {
			return nil, fmt.Errorf("read activity for preview: %w", err)
		}
		snapshot.Activity = &activity
	}

	snapshot.Document.Type = string(document.DocumentType)
	snapshot.Document.TypeHebrew = document.DocumentType.HebrewName()
	// No series and no number: a preview has consumed nothing.
	snapshot.Document.FullNumber = previewNumber
	snapshot.Document.DocumentDate = document.DocumentDate
	if document.DueDate != nil {
		snapshot.Document.DueDate = *document.DueDate
	}
	snapshot.Document.Currency = money.Currency
	snapshot.Document.Notes = document.Notes
	if rules.RequiresExemptDealerWording() {
		snapshot.Document.RequiredWording = "עוסק פטור"
	}

	for _, line := range totals.Lines {
		snapshot.Lines = append(snapshot.Lines, struct {
			LineNumber    int    `json:"line_number"`
			Description   string `json:"description"`
			Unit          string `json:"unit"`
			QuantityMilli int64  `json:"quantity_milli"`
			UnitPrice     int64  `json:"unit_price_agorot"`
			LineTotal     int64  `json:"line_total_agorot"`
		}{
			LineNumber:    line.LineNumber,
			Description:   line.Description,
			Unit:          line.Unit,
			QuantityMilli: line.QuantityMilli,
			UnitPrice:     line.UnitPrice.Agorot(),
			LineTotal:     line.LineTotal.Agorot(),
		})
	}

	snapshot.Totals.Subtotal = totals.Subtotal.Agorot()
	snapshot.Totals.VAT = totals.VAT.Agorot()
	snapshot.Totals.Total = totals.Total.Agorot()

	snapshot.IssuedAt = time.Now().UTC()
	snapshot.TemplateVersion = tmpl.CurrentDocumentVersion
	snapshot.BusinessMode = string(rules.Mode)

	return snapshot, nil
}

// renderPreview turns a preview snapshot into PDF bytes.
func (s *Service) renderPreview(ctx context.Context, snapshot *Snapshot) ([]byte, string, error) {
	html, err := RenderHTML(snapshot, s.location, RenderOptions{
		Preview: true,
		Logo:    s.logoFor(snapshot),
	})
	if err != nil {
		return nil, "", fmt.Errorf("render preview: %w", err)
	}

	rendered, err := s.renderer.Render(ctx, html)
	if err != nil {
		return nil, "", err
	}

	return rendered.Bytes, "preview-" + time.Now().Format("20060102-150405") + ".pdf", nil
}
