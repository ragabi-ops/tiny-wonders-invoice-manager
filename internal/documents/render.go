package documents

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	tmpl "github.com/ragabix/tiny-wonders-invoice-manager/templates"
)

// LogoImage is the business mark, already loaded, ready to be inlined.
type LogoImage struct {
	Bytes []byte
}

// businessView is the business block as printed.
type businessView struct {
	DisplayName  string
	ContactBlock string
	FooterNote   string
	// LogoDataURI is the logo embedded in the page. The renderer has no network
	// access, so the image travels inside the HTML or not at all.
	LogoDataURI template.URL
}

// customerView is the customer block as printed.
type customerView struct {
	DisplayName string
	DetailBlock string
}

// lineView is one printed line, already formatted.
type lineView struct {
	Description string
	Unit        string
	Quantity    string
	UnitPrice   string
	LineTotal   string
}

// settledView is one document a payment settled, as printed.
type settledView struct {
	Title      string
	FullNumber string
	Amount     string
}

// paymentView is the payment block on a receipt, already formatted.
type paymentView struct {
	Amount           string
	MethodHebrew     string
	ReceivedAt       string
	Reference        string
	SettledDocuments []settledView
	OnAccount        string
}

// templateData is what the document template renders. Every field is already a
// string: the template formats nothing, so what is printed is exactly what the
// snapshot says (plan.md 11).
type templateData struct {
	DocumentTitle  string
	FullNumber     string
	DocumentDate   string
	DueDate        string
	Business       businessView
	Customer       customerView
	ActivityName   string
	Lines          []lineView
	Subtotal       string
	VAT            string
	ShowVAT        bool
	Total          string
	Notes          string
	PaymentDetails string
	Payment        *paymentView
	// WatermarkText and BannerText drive the diagonal stamp and the banner.
	// Empty means neither is drawn — a real, live document.
	WatermarkText   string
	BannerText      string
	PreviewMode     bool
	RequiredWording string
	IssuedAt        string
	TemplateVersion string
	// TemplateLabel is TemplateVersion without its directory, so the footer of a
	// customer-facing document reads "v4" instead of the internal path
	// "document/v4". The full value stays in the snapshot for auditing.
	TemplateLabel string
	TestMode      bool
	Cancelled     bool
}

// RenderOptions is the present-day state a rendering may show, on top of what
// the snapshot itself says.
type RenderOptions struct {
	// Cancelled adds the cancellation stamp.
	Cancelled bool
	// Preview marks the output as a draft that has not been issued: no official
	// number, no legal standing, nothing stored.
	Preview bool
	// Logo is the business mark, already loaded.
	Logo *LogoImage
}

// RenderHTML builds the printable HTML of a document from its snapshot alone.
//
// It reads nothing from the live tables: re-rendering a document issued last
// year must produce last year's content even if the customer has since moved
// (plan.md 11, 14). RenderOptions carries the only present-day state shown, and
// each item only adds a stamp.
func RenderHTML(snapshot *Snapshot, location *time.Location, options RenderOptions) (string, error) {
	version := snapshot.TemplateVersion
	if version == "" {
		return "", fmt.Errorf("snapshot has no template version")
	}

	parsed, err := template.ParseFS(tmpl.FS, version+".html")
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", version, err)
	}

	data := templateData{
		DocumentTitle:   snapshot.Document.TypeHebrew,
		FullNumber:      snapshot.Document.FullNumber,
		DocumentDate:    snapshot.Document.DocumentDate,
		DueDate:         snapshot.Document.DueDate,
		RequiredWording: snapshot.Document.RequiredWording,
		Notes:           snapshot.Document.Notes,
		PaymentDetails:  snapshot.Business.BankDetails,
		TemplateVersion: version,
		TemplateLabel:   version[strings.LastIndex(version, "/")+1:],
		TestMode:        snapshot.Document.TestMode,
		Cancelled:       options.Cancelled,
		PreviewMode:     options.Preview,
		Business: businessView{
			DisplayName:  firstNonEmpty(snapshot.Business.DisplayName, snapshot.Business.LegalName),
			ContactBlock: businessContact(snapshot),
			FooterNote:   snapshot.Business.FooterNote,
			LogoDataURI:  logoDataURI(options.Logo),
		},
		Customer: customerView{
			DisplayName: snapshot.Customer.DisplayName,
			DetailBlock: customerDetail(snapshot),
		},
		Subtotal: formatILS(snapshot.Totals.Subtotal),
		VAT:      formatILS(snapshot.Totals.VAT),
		ShowVAT:  snapshot.Totals.VAT != 0,
		Total:    formatILS(snapshot.Totals.Total),
		IssuedAt: snapshot.IssuedAt.In(location).Format("02/01/2006 15:04"),
	}

	if snapshot.Activity != nil {
		data.ActivityName = snapshot.Activity.Name
	}

	// A preview is the louder warning: it is the one thing on screen that is
	// not a document at all, so it says so first.
	switch {
	case options.Preview:
		data.WatermarkText = "טיוטה"
		data.BannerText = "תצוגה מקדימה — המסמך טרם הופק, אין לו מספר ואינו נשמר. " +
			"כך הוא ייראה לאחר ההפקה."
	case snapshot.Document.TestMode:
		data.WatermarkText = "מסמך לבדיקה בלבד"
		data.BannerText = "מסמך לבדיקה בלבד — אינו מסמך רשמי ואין לו תוקף חשבונאי או משפטי."
	}

	if snapshot.Payment != nil {
		payment := &paymentView{
			Amount:       formatILS(snapshot.Payment.AmountAgorot),
			MethodHebrew: snapshot.Payment.MethodHebrew,
			ReceivedAt:   snapshot.Payment.ReceivedAt,
			Reference:    snapshot.Payment.Reference,
		}
		for _, settled := range snapshot.Payment.SettledDocuments {
			payment.SettledDocuments = append(payment.SettledDocuments, settledView{
				Title:      settled.TypeHebrew,
				FullNumber: settled.FullNumber,
				Amount:     formatILS(settled.AmountAgorot),
			})
		}
		if snapshot.Payment.OnAccountAgorot > 0 {
			payment.OnAccount = formatILS(snapshot.Payment.OnAccountAgorot)
		}
		data.Payment = payment
	}

	for _, line := range snapshot.Lines {
		data.Lines = append(data.Lines, lineView{
			Description: line.Description,
			Unit:        line.Unit,
			Quantity:    money.FormatQuantityMilli(line.QuantityMilli),
			UnitPrice:   formatILS(line.UnitPrice),
			LineTotal:   formatILS(line.LineTotal),
		})
	}

	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render template %q: %w", version, err)
	}
	return out.String(), nil
}

// formatILS renders agorot as ₪1,234.56 for print. Grouping is done here rather
// than by a locale library so a stored document always prints identically,
// whatever the server's locale data happens to be.
func formatILS(agorot int64) string {
	amount := money.FromAgorot(agorot)

	plain := amount.String() // e.g. "-1234.56"
	negative := strings.HasPrefix(plain, "-")
	plain = strings.TrimPrefix(plain, "-")

	whole, frac, _ := strings.Cut(plain, ".")

	var grouped strings.Builder
	for index, digit := range whole {
		if index > 0 && (len(whole)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}

	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s₪%s.%s", sign, grouped.String(), frac)
}

func businessContact(snapshot *Snapshot) string {
	var parts []string
	if snapshot.Business.BusinessNumber != "" {
		parts = append(parts, "מספר עוסק: "+snapshot.Business.BusinessNumber)
	}
	if snapshot.Business.Address != "" {
		parts = append(parts, snapshot.Business.Address)
	}
	if snapshot.Business.Phone != "" {
		parts = append(parts, "טלפון: "+snapshot.Business.Phone)
	}
	if snapshot.Business.Email != "" {
		parts = append(parts, snapshot.Business.Email)
	}
	if snapshot.Business.Website != "" {
		parts = append(parts, snapshot.Business.Website)
	}
	return strings.Join(parts, "\n")
}

func customerDetail(snapshot *Snapshot) string {
	var parts []string
	if snapshot.Customer.LegalName != "" && snapshot.Customer.LegalName != snapshot.Customer.DisplayName {
		parts = append(parts, snapshot.Customer.LegalName)
	}
	if snapshot.Customer.BusinessOrIDNumber != "" {
		parts = append(parts, "מספר עוסק / ת״ז: "+snapshot.Customer.BusinessOrIDNumber)
	}
	if snapshot.Customer.Address != "" {
		parts = append(parts, snapshot.Customer.Address)
	}
	if snapshot.Customer.Phone != "" {
		parts = append(parts, snapshot.Customer.Phone)
	}
	if snapshot.Customer.Email != "" {
		parts = append(parts, snapshot.Customer.Email)
	}
	return strings.Join(parts, "\n")
}

// logoDataURI encodes the logo for embedding. The media type comes from the
// bytes, not from anything a user typed, and the result is marked as a trusted
// URL because this function — not the template data — constructed it.
func logoDataURI(logo *LogoImage) template.URL {
	if logo == nil || len(logo.Bytes) == 0 {
		return ""
	}

	mediaType := http.DetectContentType(logo.Bytes)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return ""
	}

	return template.URL("data:" + mediaType + ";base64," +
		base64.StdEncoding.EncodeToString(logo.Bytes))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
