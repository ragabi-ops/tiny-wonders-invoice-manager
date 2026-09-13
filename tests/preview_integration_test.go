package tests

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestPreviewShowsTheDocumentWithoutIssuingIt is the point of the feature:
// issuance is irreversible, so the operator gets one cheap look first — and
// that look must cost nothing.
func TestPreviewShowsTheDocumentWithoutIssuingIt(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "סדנת יצירה", "unit": "שעה", "quantity_milli": 2500, "unit_price_agorot": 12000},
	})

	resp := c.doRawGet("/api/v1/documents/" + draft.ID + "/preview")
	if resp.status != http.StatusOK {
		t.Fatalf("preview = %d %s, want 200", resp.status, resp.body)
	}
	if !bytes.HasPrefix(resp.body, []byte("%PDF-")) {
		t.Fatalf("preview is not a PDF (starts %q)", firstBytes(resp.body, 8))
	}
	// Shown in the browser, not downloaded into a file the operator must find
	// and then delete.
	if disposition := resp.header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "inline;") {
		t.Errorf("Content-Disposition = %q, want inline", disposition)
	}

	ctx := context.Background()

	// Nothing may have been consumed or stored.
	var state string
	var number *int64
	var snapshot *string
	if err := h.pool.QueryRow(ctx,
		`SELECT state, number, snapshot::text FROM documents WHERE id = $1`, draft.ID).
		Scan(&state, &number, &snapshot); err != nil {
		t.Fatalf("read document: %v", err)
	}
	if state != "DRAFT" || number != nil || snapshot != nil {
		t.Fatalf("after a preview the document is %s with number %v and snapshot %v; "+
			"a preview must consume and store nothing", state, number, snapshot)
	}

	var sequences int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM document_sequences`).Scan(&sequences); err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	if sequences != 0 {
		t.Fatalf("a preview created %d sequence counters; it must allocate nothing", sequences)
	}

	// The document must still be editable and deletable afterwards.
	if resp := c.put("/api/v1/documents/"+draft.ID, map[string]any{
		"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
		"document_date": "2026-09-08",
		"lines":         []map[string]any{{"description": "אחרי תצוגה", "quantity_milli": 1000, "unit_price_agorot": 500}},
	}); resp.status != http.StatusOK {
		t.Fatalf("the draft is no longer editable after a preview: %d %s", resp.status, resp.body)
	}
}

func TestPreviewIsMarkedAsADraftAndCarriesNoNumber(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := c.put("/api/v1/settings/business-profile", map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "טיני וונדרס",
		"business_number": "515123456", "address": "", "phone": "", "email": "",
		"website": "", "bank_details": "", "footer_note": "",
	}); resp.status != http.StatusOK {
		t.Fatalf("set business profile = %d %s", resp.status, resp.body)
	}

	customer := createCustomer(t, c, map[string]any{"display_name": "נועה כהן"})
	draft := createDraft(t, c, customer.ID, []map[string]any{
		{"description": "סדנת יצירה", "unit": "שעה", "quantity_milli": 1500, "unit_price_agorot": 12000},
	})

	resp := c.doRawGet("/api/v1/documents/" + draft.ID + "/preview")
	if resp.status != http.StatusOK {
		t.Fatalf("preview = %d", resp.status)
	}

	text, err := pdfText(resp.body)
	if err != nil {
		t.Skipf("pdftotext unavailable: %v", err)
	}

	// A printed preview must never be mistakable for the document.
	if !strings.Contains(text, "טיוטה") {
		t.Errorf("the preview carries no draft marking; extracted text:\n%s", text)
	}
	if !strings.Contains(text, "תצוגה מקדימה") {
		t.Errorf("the preview has no explanatory banner; extracted text:\n%s", text)
	}
	// It must show the real content, or it is not a useful check.
	for _, want := range []string{"נועה כהן", "סדנת יצירה", "180.00", "טיני וונדרס"} {
		if !strings.Contains(text, want) {
			t.Errorf("the preview does not show %q", want)
		}
	}
	// And it must not invent a number.
	if strings.Contains(text, "A-0000") || strings.Contains(text, "TEST-0000") {
		t.Errorf("the preview shows an official-looking number; it has none:\n%s", text)
	}
}

func TestPreviewOfAnIssuedDocumentIsRefused(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	invoice := issuedInvoice(t, c, h.aCustomer(t, c), 10_000)

	// An issued document has a real PDF; a watermarked re-render of it would
	// only confuse.
	if resp := c.get("/api/v1/documents/" + invoice.ID + "/preview"); resp.status != http.StatusConflict {
		t.Fatalf("preview of an issued document = %d %s, want 409", resp.status, resp.body)
	}
}

func TestPreviewOfAnEmptyDraftIsRefused(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// A draft always has at least one line, so build one and strip it in SQL to
	// reach the state a preview must refuse.
	draft := createDraft(t, c, h.aCustomer(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})
	if _, err := h.pool.Exec(context.Background(),
		`DELETE FROM document_lines WHERE document_id = $1`, draft.ID); err != nil {
		t.Fatalf("strip lines: %v", err)
	}

	if resp := c.get("/api/v1/documents/" + draft.ID + "/preview"); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("preview of an empty draft = %d %s, want 422", resp.status, resp.body)
	}
}

// TestReceiptPreviewMatchesTheReceiptThatIsIssued is the guarantee that makes
// the preview worth having: what the operator approves is what gets issued.
func TestReceiptPreviewMatchesTheReceiptThatIsIssued(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 40_000)

	body := map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 50_000,
		"received_at":   "2026-09-08",
		"method":        "BIT",
		"reference":     "בקשה 4471",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 40_000}},
	}

	preview := c.post("/api/v1/payments/preview", body)
	if preview.status != http.StatusOK {
		t.Fatalf("receipt preview = %d %s, want 200", preview.status, preview.body)
	}
	if !bytes.HasPrefix(preview.body, []byte("%PDF-")) {
		t.Fatal("the receipt preview is not a PDF")
	}

	// Previewing must record nothing at all.
	var payments int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments`).Scan(&payments); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if payments != 0 {
		t.Fatalf("%d payments were recorded by a preview", payments)
	}

	previewText, err := pdfText(preview.body)
	if err != nil {
		t.Skipf("pdftotext unavailable: %v", err)
	}

	for _, want := range []string{"טיוטה", "ביט", "בקשה 4471", invoice.FullNumber, "על החשבון"} {
		if !strings.Contains(previewText, want) {
			t.Errorf("the receipt preview does not show %q:\n%s", want, previewText)
		}
	}

	// Now record it for real and compare what the receipt actually says.
	payment := recordPayment(t, c, body)
	if payment.ReceiptDocumentID == nil {
		t.Fatal("no receipt was issued")
	}

	issued := c.do(http.MethodGet, "/api/v1/documents/"+*payment.ReceiptDocumentID+"/pdf", nil, false)
	issuedText, err := pdfText(issued.body)
	if err != nil {
		t.Skipf("pdftotext unavailable: %v", err)
	}

	// Same substance, different marking: the preview says draft, the issued one
	// carries a number.
	for _, want := range []string{"ביט", "בקשה 4471", invoice.FullNumber, "על החשבון", "₪500.00"} {
		if !strings.Contains(issuedText, want) {
			t.Errorf("the issued receipt does not show %q, but the preview promised it", want)
		}
	}
	if strings.Contains(issuedText, "טיוטה") {
		t.Error("the issued receipt is marked as a draft")
	}
	if payment.ReceiptNumber == nil || !strings.Contains(issuedText, *payment.ReceiptNumber) {
		t.Error("the issued receipt does not show its own number")
	}
}

func TestReceiptPreviewValidatesLikeTheRealThing(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	// A preview that accepted nonsense would give false confidence.
	if resp := c.post("/api/v1/payments/preview", map[string]any{
		"customer_id": customerID, "amount_agorot": 0,
		"received_at": "2026-09-08", "method": "CASH",
	}); resp.status != http.StatusUnprocessableEntity {
		t.Errorf("preview of a zero payment = %d, want 422", resp.status)
	}

	invoice := issuedInvoice(t, c, customerID, 10_000)
	if resp := c.post("/api/v1/payments/preview", map[string]any{
		"customer_id": customerID, "amount_agorot": 5_000,
		"received_at": "2026-09-08", "method": "CASH",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 9_000}},
	}); resp.status != http.StatusUnprocessableEntity {
		t.Errorf("preview allocating more than arrived = %d, want 422", resp.status)
	}
}

func TestReadOnlyRoleCanPreviewButNotIssue(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, owner)
	draft := createDraft(t, owner, customerID, []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})

	const email = "viewer-preview@example.test"
	const password = "viewer-password-here"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "צופה", "password": password, "roles": []string{"READ_ONLY"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create read-only user = %d %s", resp.status, resp.body)
	}

	viewer := h.newClient()
	viewer.loginOK(email, password)

	// Looking is reading, and it changes nothing.
	if resp := viewer.get("/api/v1/documents/" + draft.ID + "/preview"); resp.status != http.StatusOK {
		t.Errorf("read-only preview = %d, want 200", resp.status)
	}
	// Issuing is not.
	if resp := viewer.post("/api/v1/documents/"+draft.ID+"/issue", nil); resp.status != http.StatusForbidden {
		t.Errorf("read-only issue = %d, want 403", resp.status)
	}
	// Nor is previewing a receipt, which is part of recording a payment.
	if resp := viewer.post("/api/v1/payments/preview", map[string]any{
		"customer_id": customerID, "amount_agorot": 100,
		"received_at": "2026-09-08", "method": "CASH",
	}); resp.status != http.StatusForbidden {
		t.Errorf("read-only receipt preview = %d, want 403", resp.status)
	}
}
