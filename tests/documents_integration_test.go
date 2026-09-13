package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// issueDraft issues a draft and fails the test if it does not become issued.
func issueDraft(t *testing.T, c *client, id string) draftFixture {
	t.Helper()
	resp := c.post("/api/v1/documents/"+id+"/issue", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("issue = %d %s, want 200", resp.status, resp.body)
	}
	var issued draftFixture
	resp.decode(t, &issued)
	return issued
}

func TestTotalsAreComputedByTheServer(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	customerID := h.aCustomer(t, c)

	// 2.5 x 120.00 = 300.00, plus 1 x 45.50.
	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "סדנה", "unit": "שעה", "quantity_milli": 2500, "unit_price_agorot": 12000},
		{"description": "חומרים", "quantity_milli": 1000, "unit_price_agorot": 4550},
	})

	if draft.Subtotal != 34_550 {
		t.Fatalf("subtotal = %d agorot, want 34550", draft.Subtotal)
	}
	if draft.Total != 34_550 {
		t.Fatalf("total = %d agorot, want 34550", draft.Total)
	}
	// Exempt dealers never charge VAT (plan.md 2).
	if draft.VAT != 0 {
		t.Fatalf("vat = %d agorot, want 0 in exempt-dealer mode", draft.VAT)
	}
}

func TestClientSuppliedTotalsAreRejectedOutright(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// The request body has no place for totals, so a client that tries to send
	// one is refused rather than quietly ignored (plan.md 3.2).
	resp := c.post("/api/v1/documents/", map[string]any{
		"document_type":   "TRANSACTION_INVOICE",
		"customer_id":     h.aCustomer(t, c),
		"document_date":   "2026-09-07",
		"lines":           []map[string]any{{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100}},
		"total_agorot":    999_999,
		"subtotal_agorot": 999_999,
	})
	if resp.status != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400 for client-supplied totals", resp.status, resp.body)
	}
}

func TestDraftValidation(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{
			"no lines",
			map[string]any{"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
				"document_date": "2026-09-07", "lines": []map[string]any{}},
			"lines",
		},
		{
			"no customer",
			map[string]any{"document_type": "TRANSACTION_INVOICE", "customer_id": "",
				"document_date": "2026-09-07",
				"lines":         []map[string]any{{"description": "x", "quantity_milli": 1000, "unit_price_agorot": 1}}},
			"customer_id",
		},
		{
			"bad date",
			map[string]any{"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
				"document_date": "07/09/2026",
				"lines":         []map[string]any{{"description": "x", "quantity_milli": 1000, "unit_price_agorot": 1}}},
			"document_date",
		},
		{
			"zero quantity",
			map[string]any{"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
				"document_date": "2026-09-07",
				"lines":         []map[string]any{{"description": "x", "quantity_milli": 0, "unit_price_agorot": 100}}},
			"lines.0.quantity_milli",
		},
		{
			"unknown type",
			map[string]any{"document_type": "TAX_INVOICE", "customer_id": customerID,
				"document_date": "2026-09-07",
				"lines":         []map[string]any{{"description": "x", "quantity_milli": 1000, "unit_price_agorot": 1}}},
			"document_type",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := c.post("/api/v1/documents/", testCase.body)
			if resp.status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d %s, want 422", resp.status, resp.body)
			}

			var envelope struct {
				Error struct {
					Details map[string]string `json:"details"`
				} `json:"error"`
			}
			resp.decode(t, &envelope)

			message, ok := envelope.Error.Details[testCase.field]
			if !ok {
				t.Fatalf("details = %v, want a message for %q", envelope.Error.Details, testCase.field)
			}
			if !containsHebrew(message) {
				t.Errorf("message %q is not Hebrew", message)
			}
		})
	}
}

func TestExemptDealerCanNeverIssueATaxInvoice(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// TAX_INVOICE (חשבונית מס) is not a type this deployment knows, and the
	// compliance adapter refuses it in exempt-dealer mode (plan.md 2).
	var types struct {
		Types []struct {
			Type string `json:"type"`
		} `json:"types"`
	}
	c.get("/api/v1/documents/types").decode(t, &types)
	for _, documentType := range types.Types {
		if documentType.Type == "TAX_INVOICE" {
			t.Fatal("TAX_INVOICE is offered as a document type in exempt-dealer mode")
		}
	}

	resp := c.post("/api/v1/documents/", map[string]any{
		"document_type": "TAX_INVOICE",
		"customer_id":   h.aCustomer(t, c),
		"document_date": "2026-09-07",
		"lines":         []map[string]any{{"description": "x", "quantity_milli": 1000, "unit_price_agorot": 100}},
	})
	if resp.status == http.StatusCreated {
		t.Fatal("a tax invoice draft was created in exempt-dealer mode")
	}
}

func TestDraftsAreEditableAndDeletable(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "ראשוני", "quantity_milli": 1000, "unit_price_agorot": 10000},
	})
	if draft.State != "DRAFT" || draft.FullNumber != "" {
		t.Fatalf("a new document is %s with number %q, want DRAFT and no number", draft.State, draft.FullNumber)
	}

	resp := c.put("/api/v1/documents/"+draft.ID, map[string]any{
		"document_type": "TRANSACTION_INVOICE",
		"customer_id":   customerID,
		"document_date": "2026-09-08",
		"lines": []map[string]any{
			{"description": "מעודכן", "quantity_milli": 2000, "unit_price_agorot": 10000},
		},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("update draft = %d %s", resp.status, resp.body)
	}
	var updated draftFixture
	resp.decode(t, &updated)
	if updated.Total != 20_000 {
		t.Fatalf("total after update = %d, want 20000", updated.Total)
	}

	if resp := c.do(http.MethodDelete, "/api/v1/documents/"+draft.ID, nil, true); resp.status != http.StatusNoContent {
		t.Fatalf("delete draft = %d %s, want 204", resp.status, resp.body)
	}
	if resp := c.get("/api/v1/documents/" + draft.ID); resp.status != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", resp.status)
	}
}

func TestIssuedDocumentIsImmutable(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "סדנה", "quantity_milli": 1000, "unit_price_agorot": 15000},
	})
	issued := issueDraft(t, c, draft.ID)

	if issued.State != "ISSUED" || issued.FullNumber == "" {
		t.Fatalf("issued document is %s with number %q", issued.State, issued.FullNumber)
	}

	// The API must refuse to edit or delete it.
	if resp := c.put("/api/v1/documents/"+draft.ID, map[string]any{
		"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
		"document_date": "2026-09-07",
		"lines":         []map[string]any{{"description": "שונה", "quantity_milli": 1000, "unit_price_agorot": 1}},
	}); resp.status != http.StatusConflict {
		t.Fatalf("update issued = %d %s, want 409", resp.status, resp.body)
	}
	if resp := c.do(http.MethodDelete, "/api/v1/documents/"+draft.ID, nil, true); resp.status != http.StatusConflict {
		t.Fatalf("delete issued = %d %s, want 409", resp.status, resp.body)
	}

	// And so must the database, even for SQL that bypasses the API entirely
	// (plan.md 3.1).
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx,
		`UPDATE documents SET total_agorot = 1 WHERE id = $1`, draft.ID); err == nil {
		t.Fatal("the database accepted an UPDATE to an issued document's total")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, draft.ID); err == nil {
		t.Fatal("the database accepted a DELETE of an issued document")
	}
	if _, err := h.pool.Exec(ctx,
		`UPDATE document_lines SET description = 'שונה' WHERE document_id = $1`, draft.ID); err == nil {
		t.Fatal("the database accepted an UPDATE to an issued document's lines")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM document_lines WHERE document_id = $1`, draft.ID); err == nil {
		t.Fatal("the database accepted a DELETE of an issued document's lines")
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO document_lines (document_id, line_number, description, quantity_milli,
		                            unit_price_agorot, line_total_agorot)
		VALUES ($1, 99, 'שורה מוברחת', 1000, 100, 100)`, draft.ID); err == nil {
		t.Fatal("the database accepted a new line on an issued document")
	}
}

func TestIssuanceStoresAnImmutableSnapshot(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// Set up a business profile and a customer with recognizable details.
	if resp := c.put("/api/v1/settings/business-profile", map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "Tiny Wonders",
		"business_number": "515123456", "address": "תל אביב", "phone": "050-1112222",
		"email": "hello@example.test", "website": "", "bank_details": "בנק 12 סניף 345",
		"footer_note": "תודה!",
	}); resp.status != http.StatusOK {
		t.Fatalf("set business profile = %d %s", resp.status, resp.body)
	}

	customer := createCustomer(t, c, map[string]any{
		"display_name": "נועה כהן", "phone": "050-123-4567", "address": "רחוב הרצל 5",
	})

	draft := createDraft(t, c, customer.ID, []map[string]any{
		{"description": "סדנת יצירה", "unit": "שעה", "quantity_milli": 1500, "unit_price_agorot": 12000},
	})
	issued := issueDraft(t, c, draft.ID)

	var snapshot struct {
		SchemaVersion int `json:"schema_version"`
		Business      struct {
			LegalName      string `json:"legal_name"`
			BusinessNumber string `json:"business_number"`
		} `json:"business"`
		Customer struct {
			DisplayName string `json:"display_name"`
			Phone       string `json:"phone"`
			Address     string `json:"address"`
		} `json:"customer"`
		Document struct {
			FullNumber      string `json:"full_number"`
			RequiredWording string `json:"required_wording"`
			TestMode        bool   `json:"test_mode"`
		} `json:"document"`
		Lines []struct {
			Description   string `json:"description"`
			QuantityMilli int64  `json:"quantity_milli"`
			LineTotal     int64  `json:"line_total_agorot"`
		} `json:"lines"`
		Totals struct {
			Total int64 `json:"total_agorot"`
		} `json:"totals"`
		BusinessMode    string `json:"business_mode"`
		TemplateVersion string `json:"template_version"`
	}
	c.get("/api/v1/documents/"+draft.ID+"/snapshot").decode(t, &snapshot)

	if snapshot.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", snapshot.SchemaVersion)
	}
	if snapshot.Business.BusinessNumber != "515123456" {
		t.Errorf("business number = %q", snapshot.Business.BusinessNumber)
	}
	if snapshot.Customer.DisplayName != "נועה כהן" || snapshot.Customer.Address != "רחוב הרצל 5" {
		t.Errorf("customer snapshot = %+v", snapshot.Customer)
	}
	if snapshot.Document.FullNumber != issued.FullNumber {
		t.Errorf("snapshot number %q differs from the document's %q",
			snapshot.Document.FullNumber, issued.FullNumber)
	}
	// 1.5 x 120.00 = 180.00
	if len(snapshot.Lines) != 1 || snapshot.Lines[0].LineTotal != 18_000 {
		t.Errorf("lines = %+v, want one line totalling 18000 agorot", snapshot.Lines)
	}
	if snapshot.Totals.Total != 18_000 {
		t.Errorf("snapshot total = %d, want 18000", snapshot.Totals.Total)
	}
	if snapshot.Document.RequiredWording != "עוסק פטור" {
		t.Errorf("required wording = %q, want עוסק פטור", snapshot.Document.RequiredWording)
	}
	if snapshot.BusinessMode != "EXEMPT_DEALER" {
		t.Errorf("business mode = %q", snapshot.BusinessMode)
	}
	if snapshot.TemplateVersion == "" {
		t.Error("the snapshot records no template version")
	}

	// Changing the customer afterwards must not change the snapshot: a
	// historical document is never reconstructed from mutable tables
	// (plan.md 11).
	if resp := c.put("/api/v1/customers/"+customer.ID, map[string]any{
		"customer_type": "PERSON", "display_name": "שם אחר לגמרי",
		"legal_name": "", "business_or_id_number": "", "phone": "052-999-9999",
		"email": "", "address": "כתובת אחרת", "notes": "", "preferred_delivery": "NONE",
	}); resp.status != http.StatusOK {
		t.Fatalf("update customer = %d %s", resp.status, resp.body)
	}

	var after struct {
		Customer struct {
			DisplayName string `json:"display_name"`
			Address     string `json:"address"`
		} `json:"customer"`
	}
	c.get("/api/v1/documents/"+draft.ID+"/snapshot").decode(t, &after)

	if after.Customer.DisplayName != "נועה כהן" || after.Customer.Address != "רחוב הרצל 5" {
		t.Fatalf("the snapshot followed the customer's later edits: %+v", after.Customer)
	}
}

func TestIssuanceProducesAStoredHebrewPDF(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := c.put("/api/v1/settings/business-profile", map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "טיני וונדרס",
		"business_number": "515123456", "address": "תל אביב", "phone": "050-1112222",
		"email": "hello@example.test", "website": "", "bank_details": "",
		"footer_note": "תודה רבה",
	}); resp.status != http.StatusOK {
		t.Fatalf("set business profile = %d %s", resp.status, resp.body)
	}

	customer := createCustomer(t, c, map[string]any{"display_name": "נועה כהן"})
	draft := createDraft(t, c, customer.ID, []map[string]any{
		{"description": "סדנת יצירה", "unit": "שעה", "quantity_milli": 1500, "unit_price_agorot": 12000},
	})
	issued := issueDraft(t, c, draft.ID)

	resp := c.do(http.MethodGet, "/api/v1/documents/"+draft.ID+"/pdf", nil, false)
	if resp.status != http.StatusOK {
		t.Fatalf("download pdf = %d, want 200", resp.status)
	}
	if !bytes.HasPrefix(resp.body, []byte("%PDF-")) {
		t.Fatalf("the downloaded artifact is not a PDF (first bytes %q)", firstBytes(resp.body, 16))
	}
	if len(resp.body) < 2000 {
		t.Fatalf("the PDF is only %d bytes; it is probably blank", len(resp.body))
	}

	// The stored hash must match what the database recorded, which is what makes
	// tampering detectable (plan.md 14).
	var recordedSHA string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT pdf_sha256 FROM documents WHERE id = $1`, draft.ID).Scan(&recordedSHA); err != nil {
		t.Fatalf("read recorded hash: %v", err)
	}
	if recordedSHA == "" {
		t.Fatal("no PDF hash was recorded")
	}

	// Hebrew must actually be in the document, not tofu boxes. pdftotext is
	// optional: when it is missing the structural checks above still ran.
	text, err := pdfText(resp.body)
	if err != nil {
		t.Skipf("pdftotext unavailable, skipping the text assertions: %v", err)
	}
	for _, want := range []string{"חשבונית עסקה", "נועה כהן", "סדנת יצירה", "עוסק פטור", "טיני וונדרס"} {
		if !strings.Contains(text, want) {
			t.Errorf("the PDF does not contain %q; extracted text:\n%s", want, text)
		}
	}
	// The number and the total must be printed and readable.
	if !strings.Contains(text, issued.FullNumber) {
		t.Errorf("the PDF does not show its own number %q", issued.FullNumber)
	}
	if !strings.Contains(text, "180.00") {
		t.Errorf("the PDF does not show the total 180.00; extracted text:\n%s", text)
	}
}

func TestCancellationPreservesTheOriginal(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 5000},
	})
	issued := issueDraft(t, c, draft.ID)

	// A cancellation with no reason is not auditable and is refused.
	if resp := c.post("/api/v1/documents/"+draft.ID+"/cancel",
		map[string]any{"reason": ""}); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("cancel without a reason = %d %s, want 422", resp.status, resp.body)
	}

	resp := c.post("/api/v1/documents/"+draft.ID+"/cancel",
		map[string]any{"reason": "הלקוח ביטל את ההזמנה"})
	if resp.status != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.status, resp.body)
	}

	var cancelled struct {
		State              string  `json:"state"`
		FullNumber         string  `json:"full_number"`
		Total              int64   `json:"total_agorot"`
		CancellationReason *string `json:"cancellation_reason"`
		PDFSHA256          *string `json:"pdf_sha256"`
	}
	resp.decode(t, &cancelled)

	if cancelled.State != "CANCELLED" {
		t.Fatalf("state = %q, want CANCELLED", cancelled.State)
	}
	// The number, the total and the artifact all survive (plan.md 9).
	if cancelled.FullNumber != issued.FullNumber {
		t.Errorf("number changed from %q to %q on cancellation", issued.FullNumber, cancelled.FullNumber)
	}
	if cancelled.Total != issued.Total {
		t.Errorf("total changed from %d to %d on cancellation", issued.Total, cancelled.Total)
	}
	if cancelled.PDFSHA256 == nil || *cancelled.PDFSHA256 == "" {
		t.Error("the stored PDF hash was lost on cancellation")
	}
	if cancelled.CancellationReason == nil || *cancelled.CancellationReason == "" {
		t.Error("no cancellation reason was recorded")
	}

	// The artifact is still downloadable: a cancelled document is still a
	// document that was issued.
	if resp := c.do(http.MethodGet, "/api/v1/documents/"+draft.ID+"/pdf", nil, false); resp.status != http.StatusOK {
		t.Errorf("pdf of a cancelled document = %d, want 200", resp.status)
	}

	// Cancelling twice is refused.
	if resp := c.post("/api/v1/documents/"+draft.ID+"/cancel",
		map[string]any{"reason": "שוב"}); resp.status != http.StatusConflict {
		t.Errorf("second cancel = %d, want 409", resp.status)
	}
}

func TestIssuanceIsIdempotentUnderAKey(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 7500},
	})

	const key = "retry-key-0001"

	first := c.postWithHeaders("/api/v1/documents/"+draft.ID+"/issue", nil,
		map[string]string{"Idempotency-Key": key})
	if first.status != http.StatusOK {
		t.Fatalf("first issue = %d %s", first.status, first.body)
	}
	var firstDocument draftFixture
	first.decode(t, &firstDocument)

	// The retry a flaky network would produce must return the same document,
	// not a second one with a second official number (plan.md 10).
	second := c.postWithHeaders("/api/v1/documents/"+draft.ID+"/issue", nil,
		map[string]string{"Idempotency-Key": key})
	if second.status != http.StatusOK {
		t.Fatalf("replayed issue = %d %s, want 200", second.status, second.body)
	}
	var secondDocument draftFixture
	second.decode(t, &secondDocument)

	if secondDocument.FullNumber != firstDocument.FullNumber {
		t.Fatalf("the replay returned number %q, want the original %q",
			secondDocument.FullNumber, firstDocument.FullNumber)
	}

	var count int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM documents WHERE number IS NOT NULL`).Scan(&count); err != nil {
		t.Fatalf("count issued documents: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d documents carry a number after one issuance and one replay, want 1", count)
	}
}

func TestIssuingAnAlreadyIssuedDocumentIsRefused(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})
	issueDraft(t, c, draft.ID)

	// Without an idempotency key a second issue is a genuine mistake, and must
	// not allocate a second number.
	if resp := c.post("/api/v1/documents/"+draft.ID+"/issue", nil); resp.status != http.StatusConflict {
		t.Fatalf("second issue = %d %s, want 409", resp.status, resp.body)
	}
}

func TestDocumentsAreIssuedInTheTestSeriesWhileTheGateIsClosed(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})
	issued := issueDraft(t, c, draft.ID)

	// The compliance gate is unresolved, so nothing issued here may consume an
	// official number or claim to be a legal document (plan.md 2).
	if !issued.TestMode {
		t.Fatal("a document issued while the compliance gate is closed is not marked as test mode")
	}
	if !strings.HasPrefix(issued.FullNumber, "TEST-") {
		t.Fatalf("number = %q, want the TEST series while the gate is closed", issued.FullNumber)
	}

	var snapshot struct {
		Document struct {
			TestMode bool `json:"test_mode"`
		} `json:"document"`
	}
	c.get("/api/v1/documents/"+draft.ID+"/snapshot").decode(t, &snapshot)
	if !snapshot.Document.TestMode {
		t.Error("the snapshot does not record that the document was issued in test mode")
	}

	// The PDF must say so too, so a printed copy cannot be mistaken for real.
	pdfResp := c.do(http.MethodGet, "/api/v1/documents/"+draft.ID+"/pdf", nil, false)
	text, err := pdfText(pdfResp.body)
	if err != nil {
		t.Skipf("pdftotext unavailable: %v", err)
	}
	if !strings.Contains(text, "לבדיקה") {
		t.Errorf("a test-mode PDF carries no warning; extracted text:\n%s", text)
	}
}

func TestIssuanceIsAudited(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})
	issued := issueDraft(t, c, draft.ID)

	if resp := c.post("/api/v1/documents/"+draft.ID+"/cancel",
		map[string]any{"reason": "טעות בהזנה"}); resp.status != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.status, resp.body)
	}

	var log struct {
		Events []struct {
			Operation    string          `json:"operation"`
			Reason       *string         `json:"reason"`
			AfterSummary json.RawMessage `json:"after_summary"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?entity_type=document&entity_id="+draft.ID).decode(t, &log)

	operations := map[string]json.RawMessage{}
	for _, event := range log.Events {
		operations[event.Operation] = event.AfterSummary
	}
	for _, want := range []string{"DOCUMENT_DRAFT_CREATED", "DOCUMENT_ISSUED", "DOCUMENT_CANCELLED"} {
		if _, ok := operations[want]; !ok {
			t.Errorf("audit log is missing %s; recorded: %v", want, keysOf(operations))
		}
	}
	if summary := string(operations["DOCUMENT_ISSUED"]); !strings.Contains(summary, issued.FullNumber) {
		t.Errorf("the issuance event does not record the number: %s", summary)
	}
}

func TestSequenceReportShowsUsage(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	for range 3 {
		draft := createDraft(t, c, customerID, []map[string]any{
			{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
		})
		issueDraft(t, c, draft.ID)
	}

	var report struct {
		Sequences []struct {
			DocumentType string `json:"document_type"`
			Series       string `json:"series"`
			NextNumber   int64  `json:"next_number"`
			IssuedCount  int64  `json:"issued_count"`
		} `json:"sequences"`
	}
	c.get("/api/v1/documents/sequences").decode(t, &report)

	if len(report.Sequences) == 0 {
		t.Fatal("the sequence report is empty after three issuances")
	}
	found := false
	for _, sequence := range report.Sequences {
		if sequence.DocumentType == "TRANSACTION_INVOICE" && sequence.Series == "TEST" {
			found = true
			if sequence.IssuedCount != 3 {
				t.Errorf("issued count = %d, want 3", sequence.IssuedCount)
			}
			if sequence.NextNumber != 4 {
				t.Errorf("next number = %d, want 4", sequence.NextNumber)
			}
		}
	}
	if !found {
		t.Fatalf("the report has no TRANSACTION_INVOICE/TEST row: %+v", report.Sequences)
	}
}

func TestIssuanceIsRefusedWithoutARenderer(t *testing.T) {
	h := newHarnessWithoutRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})

	// An issued document with no artifact would break plan.md 3.4, so issuance
	// must fail rather than degrade.
	resp := c.post("/api/v1/documents/"+draft.ID+"/issue", nil)
	if resp.status != http.StatusInternalServerError {
		t.Fatalf("issue without a renderer = %d %s, want 500", resp.status, resp.body)
	}

	// And nothing may have been consumed: no number, still a draft.
	var state string
	var number *int64
	if err := h.pool.QueryRow(context.Background(),
		`SELECT state, number FROM documents WHERE id = $1`, draft.ID).Scan(&state, &number); err != nil {
		t.Fatalf("read document: %v", err)
	}
	if state != "DRAFT" || number != nil {
		t.Fatalf("after a failed issuance the document is %s with number %v, want DRAFT and none", state, number)
	}

	var sequences int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT coalesce(max(next_number), 1) FROM document_sequences`).Scan(&sequences); err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	if sequences != 1 {
		t.Fatalf("the sequence advanced to %d despite a failed issuance", sequences)
	}
}

func TestReadOnlyRoleCannotIssueOrCancel(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const email = "viewer-docs@example.test"
	const password = "viewer-password-here"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "צופה", "password": password, "roles": []string{"READ_ONLY"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create read-only user = %d %s", resp.status, resp.body)
	}

	draft := createDraft(t, owner, h.aCustomer(t, owner), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})

	viewer := h.newClient()
	viewer.loginOK(email, password)

	if resp := viewer.get("/api/v1/documents/"); resp.status != http.StatusOK {
		t.Errorf("read-only list = %d, want 200", resp.status)
	}
	if resp := viewer.post("/api/v1/documents/"+draft.ID+"/issue", nil); resp.status != http.StatusForbidden {
		t.Errorf("read-only issue = %d, want 403", resp.status)
	}
	if resp := viewer.post("/api/v1/documents/"+draft.ID+"/cancel",
		map[string]any{"reason": "בדיקה"}); resp.status != http.StatusForbidden {
		t.Errorf("read-only cancel = %d, want 403", resp.status)
	}
}

// pdfText extracts text from a PDF using pdftotext, when it is installed.
func pdfText(data []byte) (string, error) {
	command := exec.Command("pdftotext", "-enc", "UTF-8", "-", "-")
	command.Stdin = bytes.NewReader(data)

	var out bytes.Buffer
	command.Stdout = &out
	if err := command.Run(); err != nil {
		return "", err
	}

	// pdftotext wraps runs of RTL text in directional marks; strip them so the
	// assertions compare the words themselves.
	text := out.String()
	for _, mark := range []string{"‪", "‫", "‬", "‎", "‏"} {
		text = strings.ReplaceAll(text, mark, "")
	}
	return text, nil
}

func firstBytes(data []byte, n int) string {
	if len(data) < n {
		n = len(data)
	}
	return string(data[:n])
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
