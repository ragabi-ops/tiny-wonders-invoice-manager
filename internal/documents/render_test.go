package documents

import (
	"strings"
	"testing"
	"time"

	tmpl "github.com/ragabix/tiny-wonders-invoice-manager/templates"
)

// jerusalem is the business timezone every rendering test uses.
func jerusalem(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	return location
}

// fixture builds a snapshot with recognizable values in every field, so a
// template that silently drops one is caught.
func fixture(templateVersion string) *Snapshot {
	snapshot := &Snapshot{SchemaVersion: snapshotSchemaVersion}

	snapshot.Business.LegalName = "טיני וונדרס בע״מ"
	snapshot.Business.DisplayName = "טיני וונדרס"
	snapshot.Business.BusinessNumber = "515123456"
	snapshot.Business.Address = "רחוב הרצל 12, תל אביב"
	snapshot.Business.Phone = "050-111-2222"
	snapshot.Business.Email = "hello@tinywonders.example"
	snapshot.Business.Website = "tinywonders.example"
	snapshot.Business.BankDetails = "בנק הפועלים · סניף 601"
	snapshot.Business.FooterNote = "תודה רבה על העסקה!"

	snapshot.Customer.ID = "11111111-1111-1111-1111-111111111111"
	snapshot.Customer.CustomerType = "PERSON"
	snapshot.Customer.DisplayName = "נועה כהן"
	snapshot.Customer.LegalName = "נועה כהן"
	snapshot.Customer.BusinessOrIDNumber = "123456789"
	snapshot.Customer.Phone = "050-123-4567"
	snapshot.Customer.Email = "noa@example.test"
	snapshot.Customer.Address = "רחוב הרצל 5"

	snapshot.Document.Type = string(TypeTransactionInvoice)
	snapshot.Document.TypeHebrew = TypeTransactionInvoice.HebrewName()
	snapshot.Document.Series = SeriesOfficial
	snapshot.Document.Number = 42
	snapshot.Document.FullNumber = FormatNumber(SeriesOfficial, 42)
	snapshot.Document.DocumentDate = "2026-09-08"
	snapshot.Document.DueDate = "2026-09-22"
	snapshot.Document.Currency = "ILS"
	snapshot.Document.Notes = "הערה כללית למסמך"
	snapshot.Document.RequiredWording = "עוסק פטור"

	snapshot.Lines = append(snapshot.Lines, struct {
		LineNumber    int    `json:"line_number"`
		Description   string `json:"description"`
		Unit          string `json:"unit"`
		QuantityMilli int64  `json:"quantity_milli"`
		UnitPrice     int64  `json:"unit_price_agorot"`
		LineTotal     int64  `json:"line_total_agorot"`
	}{
		LineNumber: 1, Description: "סדנת יצירה", Unit: "שעה",
		QuantityMilli: 2500, UnitPrice: 12000, LineTotal: 30000,
	})

	snapshot.Totals.Subtotal = 30000
	snapshot.Totals.Total = 30000

	snapshot.IssuedAt = time.Date(2026, 9, 8, 9, 30, 0, 0, time.UTC)
	snapshot.IssuedByEmail = "owner@example.test"
	snapshot.TemplateVersion = templateVersion
	snapshot.BusinessMode = "EXEMPT_DEALER"

	return snapshot
}

// TestEveryTemplateVersionStillRenders is the regression guard plan.md 19 asks
// for. Documents issued under an old template must keep rendering forever, so
// every version that has ever been used has to stay renderable — not just the
// current one.
func TestEveryTemplateVersionStillRenders(t *testing.T) {
	entries, err := tmpl.FS.ReadDir("document")
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(entries) < 4 {
		t.Fatalf("found %d templates; every version ever used must still be present", len(entries))
	}

	location := jerusalem(t)

	for _, entry := range entries {
		version := "document/" + strings.TrimSuffix(entry.Name(), ".html")

		t.Run(version, func(t *testing.T) {
			html, err := RenderHTML(fixture(version), location, RenderOptions{})
			if err != nil {
				t.Fatalf("render %s: %v", version, err)
			}

			// Structure: a document must be an RTL Hebrew page.
			for _, want := range []string{`lang="he"`, `dir="rtl"`, `<html`, `</html>`} {
				if !strings.Contains(html, want) {
					t.Errorf("%s is missing %q", version, want)
				}
			}

			// Content: every field that carries meaning must reach the page.
			for _, want := range []string{
				"הערה כללית למסמך", // the note
				"חשבונית עסקה",     // the document type
				"A-000042",         // its number
				"נועה כהן",         // the customer
				"סדנת יצירה",       // the line
				"עוסק פטור",        // the required wording
				"₪300.00",          // the total
				"2026-09-08",       // the document date
				"טיני וונדרס",      // the business
			} {
				if !strings.Contains(html, want) {
					t.Errorf("%s does not render %q", version, want)
				}
			}

			// The renderer has no network access, so a template that reaches out
			// would render with pieces missing and no error.
			for _, forbidden := range []string{"http://", "https://fonts.", "<script"} {
				if strings.Contains(html, forbidden) {
					t.Errorf("%s contains %q; templates must be self-contained and scriptless",
						version, forbidden)
				}
			}
		})
	}
}

func TestCurrentTemplateVersionExists(t *testing.T) {
	// A bumped constant pointing at a file nobody added would fail every
	// issuance at runtime rather than at build time.
	if _, err := tmpl.FS.ReadFile(tmpl.CurrentDocumentVersion + ".html"); err != nil {
		t.Fatalf("CurrentDocumentVersion is %q but that template is missing: %v",
			tmpl.CurrentDocumentVersion, err)
	}
}

func TestTestModeWatermarkAppearsOnlyInTestMode(t *testing.T) {
	location := jerusalem(t)

	live := fixture(tmpl.CurrentDocumentVersion)
	liveHTML, err := RenderHTML(live, location, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(liveHTML, "לבדיקה") {
		t.Error("a live document carries the test-mode warning")
	}

	test := fixture(tmpl.CurrentDocumentVersion)
	test.Document.TestMode = true
	testHTML, err := RenderHTML(test, location, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(testHTML, "לבדיקה") {
		t.Error("a test-mode document carries no warning; it could be mistaken for real")
	}
}

func TestCancelledDocumentIsStamped(t *testing.T) {
	html, err := RenderHTML(fixture(tmpl.CurrentDocumentVersion), jerusalem(t), RenderOptions{Cancelled: true})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(html, "מבוטל") {
		t.Error("a cancelled document is not stamped as cancelled")
	}
}

func TestReceiptRendersItsPaymentDetails(t *testing.T) {
	snapshot := fixture(tmpl.CurrentDocumentVersion)
	snapshot.Document.Type = string(TypeReceipt)
	snapshot.Document.TypeHebrew = TypeReceipt.HebrewName()
	snapshot.Payment = &PaymentSnapshot{
		ID:           "22222222-2222-2222-2222-222222222222",
		AmountAgorot: 30000,
		ReceivedAt:   "2026-09-08",
		Method:       "BIT",
		MethodHebrew: "ביט",
		Reference:    "בקשה 4471",
		SettledDocuments: []SettledDocument{{
			DocumentID:   "33333333-3333-3333-3333-333333333333",
			TypeHebrew:   "חשבונית עסקה",
			FullNumber:   "A-000041",
			AmountAgorot: 25000,
		}},
		OnAccountAgorot: 5000,
	}

	html, err := RenderHTML(snapshot, jerusalem(t), RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// A receipt that does not state what was received, how, and against what is
	// useless as evidence of payment.
	for _, want := range []string{"קבלה", "ביט", "בקשה 4471", "A-000041", "על החשבון", "₪250.00", "₪50.00"} {
		if !strings.Contains(html, want) {
			t.Errorf("the receipt does not render %q", want)
		}
	}
}

func TestLogoIsInlinedNotLinked(t *testing.T) {
	// A one-pixel PNG.
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}

	html, err := RenderHTML(fixture(tmpl.CurrentDocumentVersion), jerusalem(t), RenderOptions{Logo: &LogoImage{Bytes: png}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The renderer cannot fetch anything, so a linked logo would simply be
	// missing from the printed document.
	if !strings.Contains(html, "data:image/png;base64,") {
		t.Error("the logo is not inlined as a data URI")
	}
	if strings.Contains(html, `src="/`) || strings.Contains(html, `src="http`) {
		t.Error("the logo is referenced by URL; the renderer has no network access")
	}
}

func TestRenderRejectsAnUnknownTemplateVersion(t *testing.T) {
	snapshot := fixture("document/v99")
	if _, err := RenderHTML(snapshot, jerusalem(t), RenderOptions{}); err == nil {
		t.Fatal("rendering with a nonexistent template version should fail loudly")
	}

	empty := fixture("")
	if _, err := RenderHTML(empty, jerusalem(t), RenderOptions{}); err == nil {
		t.Fatal("a snapshot with no template version should fail rather than guess one")
	}
}

func TestFormatILS(t *testing.T) {
	cases := map[int64]string{
		0:         "₪0.00",
		5:         "₪0.05",
		100:       "₪1.00",
		123456:    "₪1,234.56",
		100000000: "₪1,000,000.00",
		-25050:    "-₪250.50",
	}
	for agorot, want := range cases {
		if got := formatILS(agorot); got != want {
			t.Errorf("formatILS(%d) = %q, want %q", agorot, got, want)
		}
	}
}
