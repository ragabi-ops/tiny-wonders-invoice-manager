package tests

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

// expenseFixture is the slice of an expense these tests read back.
type expenseFixture struct {
	ID            string `json:"id"`
	Supplier      string `json:"supplier"`
	Amount        int64  `json:"amount_agorot"`
	Category      string `json:"category"`
	PaymentMethod string `json:"payment_method"`
	Attachments   []struct {
		ID               string `json:"id"`
		OriginalFilename string `json:"original_filename"`
		ContentType      string `json:"content_type"`
		ByteSize         int64  `json:"byte_size"`
		SHA256           string `json:"sha256"`
	} `json:"attachments"`
}

func createExpense(t *testing.T, c *client, body map[string]any) expenseFixture {
	t.Helper()
	resp := c.post("/api/v1/expenses/", body)
	if resp.status != http.StatusCreated {
		t.Fatalf("create expense = %d %s, want 201", resp.status, resp.body)
	}
	var expense expenseFixture
	resp.decode(t, &expense)
	return expense
}

// uploadAttachment posts one file as multipart form data.
func uploadAttachment(t *testing.T, c *client, expenseID, filename string, content []byte) response {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("build upload: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close upload: %v", err)
	}

	return c.doRaw(http.MethodPost, "/api/v1/expenses/"+expenseID+"/attachments",
		body.Bytes(), writer.FormDataContentType())
}

// tinyPDF is the smallest thing the sniffer accepts as a PDF.
var tinyPDF = append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 64)...)

// tinyPNG is a valid PNG signature followed by filler.
var tinyPNG = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte("x"), 64)...)

func TestExpenseCreateReadUpdateDelete(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	expense := createExpense(t, c, map[string]any{
		"supplier":       "חנות יצירה",
		"expense_date":   "2026-09-05",
		"amount_agorot":  24_500,
		"category":       "חומרים",
		"payment_method": "CREDIT_CARD",
		"reference":      "1234",
	})

	if expense.Amount != 24_500 || expense.Category != "חומרים" {
		t.Fatalf("expense = %+v", expense)
	}

	resp := c.put("/api/v1/expenses/"+expense.ID, map[string]any{
		"supplier":       "חנות יצירה בע״מ",
		"expense_date":   "2026-09-05",
		"amount_agorot":  30_000,
		"category":       "חומרים",
		"payment_method": "CASH",
		"reference":      "1234",
		"notes":          "עודכן",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("update expense = %d %s", resp.status, resp.body)
	}
	var updated expenseFixture
	resp.decode(t, &updated)
	if updated.Amount != 30_000 || updated.PaymentMethod != "CASH" {
		t.Fatalf("update did not apply: %+v", updated)
	}

	// An expense is an internal record with no official number, so unlike an
	// issued document it can be deleted outright.
	if resp := c.do(http.MethodDelete, "/api/v1/expenses/"+expense.ID, nil, true); resp.status != http.StatusNoContent {
		t.Fatalf("delete expense = %d %s, want 204", resp.status, resp.body)
	}
	if resp := c.get("/api/v1/expenses/" + expense.ID); resp.status != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", resp.status)
	}
}

func TestExpenseValidation(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no supplier", map[string]any{"supplier": "  ", "expense_date": "2026-09-05", "amount_agorot": 100}, "supplier"},
		{"zero amount", map[string]any{"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 0}, "amount_agorot"},
		{"bad date", map[string]any{"supplier": "ספק", "expense_date": "05/09/2026", "amount_agorot": 100}, "expense_date"},
		{
			"unknown method",
			map[string]any{"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 100, "payment_method": "GOLD"},
			"payment_method",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := c.post("/api/v1/expenses/", testCase.body)
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

func TestAttachmentUploadAndDownload(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	expense := createExpense(t, c, map[string]any{
		"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 5_000,
	})

	resp := uploadAttachment(t, c, expense.ID, "קבלה מהספק.pdf", tinyPDF)
	if resp.status != http.StatusCreated {
		t.Fatalf("upload = %d %s, want 201", resp.status, resp.body)
	}

	var attachment struct {
		ID               string `json:"id"`
		OriginalFilename string `json:"original_filename"`
		ContentType      string `json:"content_type"`
		SHA256           string `json:"sha256"`
	}
	resp.decode(t, &attachment)

	if attachment.ContentType != "application/pdf" {
		t.Errorf("content type = %q, want application/pdf", attachment.ContentType)
	}
	if attachment.OriginalFilename != "קבלה מהספק.pdf" {
		t.Errorf("filename = %q; the Hebrew name should be preserved", attachment.OriginalFilename)
	}

	download := c.do(http.MethodGet, "/api/v1/expenses/attachments/"+attachment.ID, nil, false)
	if download.status != http.StatusOK {
		t.Fatalf("download = %d, want 200", download.status)
	}
	if !bytes.Equal(download.body, tinyPDF) {
		t.Fatal("the downloaded bytes differ from what was uploaded")
	}

	// The expense now reports its attachment.
	var withAttachment expenseFixture
	c.get("/api/v1/expenses/"+expense.ID).decode(t, &withAttachment)
	if len(withAttachment.Attachments) != 1 {
		t.Fatalf("%d attachments, want 1", len(withAttachment.Attachments))
	}

	if resp := c.do(http.MethodDelete,
		"/api/v1/expenses/"+expense.ID+"/attachments/"+attachment.ID, nil, true); resp.status != http.StatusNoContent {
		t.Fatalf("remove attachment = %d %s, want 204", resp.status, resp.body)
	}
}

func TestAttachmentTypeIsDecidedByContentNotFilename(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	expense := createExpense(t, c, map[string]any{
		"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 5_000,
	})

	// A shell script named receipt.pdf must be refused: the bytes decide, not
	// the name (SECURITY.md 6).
	disguised := []byte("#!/bin/sh\necho this is not a receipt\n" + strings.Repeat("x", 64))
	if resp := uploadAttachment(t, c, expense.ID, "receipt.pdf", disguised); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("disguised script = %d %s, want 422", resp.status, resp.body)
	}

	// An HTML file is refused too: it would be the interesting one to serve
	// back into the browser.
	html := []byte("<html><body><script>alert(1)</script></body></html>")
	if resp := uploadAttachment(t, c, expense.ID, "receipt.png", html); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("html upload = %d %s, want 422", resp.status, resp.body)
	}

	// A real PNG is accepted whatever it is called.
	if resp := uploadAttachment(t, c, expense.ID, "whatever.txt", tinyPNG); resp.status != http.StatusCreated {
		t.Fatalf("png upload = %d %s, want 201", resp.status, resp.body)
	}
}

func TestAttachmentIsServedAsADownloadNeverInline(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	expense := createExpense(t, c, map[string]any{
		"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 100,
	})

	upload := uploadAttachment(t, c, expense.ID, "receipt.png", tinyPNG)
	var attachment struct {
		ID string `json:"id"`
	}
	upload.decode(t, &attachment)

	download := c.doRawGet("/api/v1/expenses/attachments/" + attachment.ID)
	if disposition := download.header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
		t.Errorf("Content-Disposition = %q, want an attachment disposition", disposition)
	}
	if nosniff := download.header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
}

func TestExpensesByCategoryReport(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	for _, expense := range []struct {
		category string
		amount   int64
	}{
		{"חומרים", 10_000},
		{"חומרים", 5_000},
		{"נסיעות", 3_000},
	} {
		createExpense(t, c, map[string]any{
			"supplier": "ספק", "expense_date": "2026-09-05",
			"amount_agorot": expense.amount, "category": expense.category,
		})
	}

	var report struct {
		Totals []struct {
			Category string `json:"category"`
			Count    int    `json:"count"`
			Total    int64  `json:"total_agorot"`
		} `json:"totals"`
	}
	c.get("/api/v1/expenses/by-category").decode(t, &report)

	byCategory := map[string]int64{}
	for _, row := range report.Totals {
		byCategory[row.Category] = row.Total
	}
	if byCategory["חומרים"] != 15_000 || byCategory["נסיעות"] != 3_000 {
		t.Fatalf("totals = %v", byCategory)
	}
}

func TestDeliveryFailureDoesNotRollBackTheIssuedDocument(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomerWithEmail(t, c)

	invoice := issuedInvoice(t, c, customerID, 20_000)

	// Email is not configured in tests, so a send request is refused outright.
	// The point of this test is what happens to the document afterwards
	// (plan.md 14).
	send := c.post("/api/v1/documents/"+invoice.ID+"/send", map[string]any{})
	if send.status == http.StatusOK {
		t.Fatal("email was reported available in a test harness with no SMTP")
	}

	var after draftFixture
	c.get("/api/v1/documents/"+invoice.ID).decode(t, &after)

	if after.State != "ISSUED" || after.FullNumber != invoice.FullNumber {
		t.Fatalf("after a failed send the document is %s numbered %q; want it untouched as %s %q",
			after.State, after.FullNumber, invoice.State, invoice.FullNumber)
	}
	if resp := c.do(http.MethodGet, "/api/v1/documents/"+invoice.ID+"/pdf", nil, false); resp.status != http.StatusOK {
		t.Fatalf("the PDF is no longer downloadable after a failed send: %d", resp.status)
	}
}

func TestWhatsAppSharePreparesAHebrewMessage(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := c.put("/api/v1/settings/business-profile", map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "טיני וונדרס",
		"business_number": "515123456", "address": "", "phone": "050-111-2222",
		"email": "", "website": "", "bank_details": "", "footer_note": "",
	}); resp.status != http.StatusOK {
		t.Fatalf("set business profile = %d %s", resp.status, resp.body)
	}

	customer := createCustomer(t, c, map[string]any{
		"display_name": "נועה כהן", "phone": "050-123-4567",
	})
	invoice := issuedInvoice(t, c, customer.ID, 30_000)

	resp := c.post("/api/v1/documents/"+invoice.ID+"/whatsapp", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("whatsapp share = %d %s", resp.status, resp.body)
	}

	var share struct {
		Phone    string `json:"phone"`
		Message  string `json:"message"`
		ShareURL string `json:"share_url"`
	}
	resp.decode(t, &share)

	if !containsHebrew(share.Message) {
		t.Errorf("the prepared message is not Hebrew: %q", share.Message)
	}
	for _, want := range []string{"נועה כהן", invoice.FullNumber, "טיני וונדרס", "300.00"} {
		if !strings.Contains(share.Message, want) {
			t.Errorf("message does not mention %q: %q", want, share.Message)
		}
	}
	// The local number must be internationalized for wa.me.
	if !strings.HasPrefix(share.ShareURL, "https://wa.me/972501234567?") {
		t.Errorf("share url = %q, want a wa.me link with the international number", share.ShareURL)
	}

	// The share is recorded, so the operator can see what was sent and when.
	var history struct {
		Attempts []struct {
			Channel string `json:"channel"`
			State   string `json:"state"`
		} `json:"attempts"`
	}
	c.get("/api/v1/documents/"+invoice.ID+"/deliveries").decode(t, &history)
	if len(history.Attempts) != 1 || history.Attempts[0].Channel != "WHATSAPP_LINK" {
		t.Fatalf("delivery history = %+v, want one WhatsApp entry", history.Attempts)
	}
}

func TestSendingADraftIsRefused(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	draft := createDraft(t, c, h.aCustomerWithEmail(t, c), []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 100},
	})

	if resp := c.post("/api/v1/documents/"+draft.ID+"/whatsapp", nil); resp.status != http.StatusConflict {
		t.Fatalf("whatsapp share of a draft = %d %s, want 409", resp.status, resp.body)
	}
}

func TestDashboardReportsRealFigures(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	today := time.Now().In(jerusalem(t)).Format("2006-01-02")

	// One invoice dated today, half paid.
	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "שירות", "quantity_milli": 1000, "unit_price_agorot": 40_000},
	})
	if resp := c.put("/api/v1/documents/"+draft.ID, map[string]any{
		"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
		"document_date": today,
		"lines":         []map[string]any{{"description": "שירות", "quantity_milli": 1000, "unit_price_agorot": 40_000}},
	}); resp.status != http.StatusOK {
		t.Fatalf("date the draft today = %d %s", resp.status, resp.body)
	}
	invoice := issueDraft(t, c, draft.ID)

	recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 15_000,
		"received_at": today, "method": "BIT",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 15_000}},
	})

	createExpense(t, c, map[string]any{
		"supplier": "ספק", "expense_date": today, "amount_agorot": 7_000, "category": "חומרים",
	})

	var dashboard struct {
		BusinessDate  string `json:"business_date"`
		RevenueToday  int64  `json:"revenue_today_agorot"`
		RevenueMonth  int64  `json:"revenue_month_agorot"`
		RevenueYear   int64  `json:"revenue_year_agorot"`
		UnpaidCount   int    `json:"unpaid_count"`
		UnpaidAmount  int64  `json:"unpaid_amount_agorot"`
		ExpensesMonth int64  `json:"expenses_month_agorot"`
		Turnover      struct {
			Revenue        int64 `json:"revenue_agorot"`
			ThresholdKnown bool  `json:"threshold_known"`
			Threshold      int64 `json:"threshold_agorot"`
		} `json:"turnover"`
		RecentDocuments []struct{ FullNumber string } `json:"recent_documents"`
		RecentPayments  []struct{ Amount int64 }      `json:"recent_payments"`
	}
	c.get("/api/v1/reports/dashboard").decode(t, &dashboard)

	if dashboard.BusinessDate != today {
		t.Errorf("business date = %q, want %q", dashboard.BusinessDate, today)
	}
	// The receipt must not be counted as revenue: it acknowledges money the
	// invoice already accounts for.
	if dashboard.RevenueToday != 40_000 {
		t.Errorf("revenue today = %d, want 40000 (the receipt must not be double-counted)", dashboard.RevenueToday)
	}
	if dashboard.RevenueMonth != 40_000 || dashboard.RevenueYear != 40_000 {
		t.Errorf("revenue month/year = %d/%d, want 40000 each", dashboard.RevenueMonth, dashboard.RevenueYear)
	}
	if dashboard.UnpaidCount != 1 || dashboard.UnpaidAmount != 25_000 {
		t.Errorf("unpaid = %d documents owing %d, want 1 owing 25000",
			dashboard.UnpaidCount, dashboard.UnpaidAmount)
	}
	if dashboard.ExpensesMonth != 7_000 {
		t.Errorf("expenses this month = %d, want 7000", dashboard.ExpensesMonth)
	}
	if !dashboard.Turnover.ThresholdKnown || dashboard.Turnover.Threshold != 12_283_300 {
		t.Errorf("turnover = %+v, want the seeded 2026 ceiling", dashboard.Turnover)
	}
	if len(dashboard.RecentDocuments) == 0 || len(dashboard.RecentPayments) == 0 {
		t.Error("the dashboard shows no recent activity despite activity existing")
	}
}

func TestRevenueBreakdowns(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	first := createCustomer(t, c, map[string]any{"display_name": "לקוח א", "confirm_duplicate": true}).ID
	second := createCustomer(t, c, map[string]any{"display_name": "לקוח ב", "confirm_duplicate": true}).ID

	issuedInvoice(t, c, first, 30_000)
	issuedInvoice(t, c, second, 20_000)

	for _, by := range []string{"period", "customer", "service", "activity"} {
		t.Run(by, func(t *testing.T) {
			var report struct {
				Rows []struct {
					Label  string `json:"label"`
					Amount int64  `json:"amount_agorot"`
				} `json:"rows"`
			}
			c.get("/api/v1/reports/revenue?by="+by).decode(t, &report)

			var total int64
			for _, row := range report.Rows {
				total += row.Amount
			}
			if total != 50_000 {
				t.Fatalf("revenue by %s totals %d, want 50000: %+v", by, total, report.Rows)
			}
		})
	}

	if resp := c.get("/api/v1/reports/revenue?by=nonsense"); resp.status != http.StatusUnprocessableEntity {
		t.Errorf("unknown breakdown = %d, want 422", resp.status)
	}
}

func TestUnpaidReportShowsOutstandingAndOverdue(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	// An invoice whose due date is well in the past.
	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "שירות", "quantity_milli": 1000, "unit_price_agorot": 60_000},
	})
	if resp := c.put("/api/v1/documents/"+draft.ID, map[string]any{
		"document_type": "TRANSACTION_INVOICE", "customer_id": customerID,
		"document_date": "2026-01-01", "due_date": "2026-01-15",
		"lines": []map[string]any{{"description": "שירות", "quantity_milli": 1000, "unit_price_agorot": 60_000}},
	}); resp.status != http.StatusOK {
		t.Fatalf("set due date = %d %s", resp.status, resp.body)
	}
	invoice := issueDraft(t, c, draft.ID)

	recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 20_000,
		"received_at": "2026-02-01", "method": "CASH",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 20_000}},
	})

	var report struct {
		Documents []struct {
			FullNumber  string `json:"full_number"`
			Total       int64  `json:"total_agorot"`
			Paid        int64  `json:"paid_agorot"`
			Outstanding int64  `json:"outstanding_agorot"`
			DaysOverdue int    `json:"days_overdue"`
		} `json:"documents"`
	}
	c.get("/api/v1/reports/unpaid").decode(t, &report)

	if len(report.Documents) != 1 {
		t.Fatalf("%d unpaid documents, want 1", len(report.Documents))
	}
	row := report.Documents[0]
	if row.Outstanding != 40_000 || row.Paid != 20_000 {
		t.Fatalf("row = %+v, want 20000 paid and 40000 outstanding", row)
	}
	if row.DaysOverdue <= 0 {
		t.Errorf("days overdue = %d, want a positive number for a due date in the past", row.DaysOverdue)
	}
}

func TestTurnoverRefusesToGuessAnUnconfiguredYear(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// 2026 has a seeded ceiling; 2020 deliberately does not, and the report
	// must say "unknown" rather than invent one (plan.md 22.3).
	var known struct {
		ThresholdKnown bool  `json:"threshold_known"`
		Threshold      int64 `json:"threshold_agorot"`
	}
	c.get("/api/v1/reports/turnover?year=2026").decode(t, &known)
	if !known.ThresholdKnown || known.Threshold != 12_283_300 {
		t.Fatalf("2026 = %+v, want the seeded ceiling", known)
	}

	var unknown struct {
		ThresholdKnown bool  `json:"threshold_known"`
		Threshold      int64 `json:"threshold_agorot"`
	}
	c.get("/api/v1/reports/turnover?year=2020").decode(t, &unknown)
	if unknown.ThresholdKnown || unknown.Threshold != 0 {
		t.Fatalf("2020 = %+v, want an unknown ceiling rather than a guessed one", unknown)
	}
}

func TestAccountantExportContainsEverything(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 50_000)
	recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 50_000,
		"received_at": "2026-09-08", "method": "BIT",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 50_000}},
	})

	expense := createExpense(t, c, map[string]any{
		"supplier": "ספק חומרים", "expense_date": "2026-09-05",
		"amount_agorot": 12_000, "category": "חומרים",
	})
	if resp := uploadAttachment(t, c, expense.ID, "קבלה.pdf", tinyPDF); resp.status != http.StatusCreated {
		t.Fatalf("upload attachment = %d %s", resp.status, resp.body)
	}

	resp := c.post("/api/v1/exports/", map[string]any{})
	if resp.status != http.StatusAccepted {
		t.Fatalf("request export = %d %s, want 202", resp.status, resp.body)
	}
	var queued struct {
		JobID string `json:"job_id"`
	}
	resp.decode(t, &queued)

	archive := waitForExport(t, c, queued.JobID)

	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}

	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}

	for _, want := range []string{"documents.csv", "payments.csv", "expenses.csv", "customers.csv", "manifest.json"} {
		if !names[want] {
			t.Errorf("the archive is missing %s; it has %v", want, keysOf(names))
		}
	}

	// Both the invoice and the receipt are in, under their own type folders:
	// each type has its own counter, so both are number 1 and would collide on
	// one flat name.
	pdfCount, attachmentCount := 0, 0
	for name := range names {
		if strings.HasPrefix(name, "pdf/") {
			pdfCount++
		}
		if strings.HasPrefix(name, "expense-attachments/") {
			attachmentCount++
		}
	}
	if pdfCount != 2 {
		t.Errorf("%d PDFs in the archive, want 2 (invoice and receipt): %v", pdfCount, keysOf(names))
	}
	if attachmentCount != 1 {
		t.Errorf("%d expense attachments, want 1", attachmentCount)
	}

	// Every CSV must open correctly in Excel on Windows, which means a BOM.
	for _, name := range []string{"documents.csv", "payments.csv", "expenses.csv", "customers.csv"} {
		content := readZipEntry(t, reader, name)
		if !bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) {
			t.Errorf("%s has no UTF-8 BOM; Excel would show mojibake", name)
		}
		if !containsHebrew(string(content)) {
			t.Errorf("%s contains no Hebrew at all", name)
		}
	}

	// The documents CSV must carry the real numbers.
	documents := string(readZipEntry(t, reader, "documents.csv"))
	if !strings.Contains(documents, invoice.FullNumber) {
		t.Errorf("documents.csv does not list %s", invoice.FullNumber)
	}
	if !strings.Contains(documents, "500.00") {
		t.Errorf("documents.csv does not show the invoice total:\n%s", documents)
	}

	manifest := readZipEntry(t, reader, "manifest.json")
	var parsed map[string]any
	if err := json.Unmarshal(manifest, &parsed); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if parsed["business_mode"] != "EXEMPT_DEALER" {
		t.Errorf("manifest business mode = %v", parsed["business_mode"])
	}
}

func TestExportEntryNamesAreUnique(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	// An invoice and a receipt both numbered 1 in the same series: they share a
	// full number and must not collide inside the archive.
	invoice := issuedInvoice(t, c, customerID, 10_000)
	payment := recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 10_000,
		"received_at": "2026-09-08", "method": "CASH",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 10_000}},
	})
	if payment.ReceiptNumber == nil || *payment.ReceiptNumber != invoice.FullNumber {
		t.Logf("invoice %s, receipt %v", invoice.FullNumber, payment.ReceiptNumber)
	}

	resp := c.post("/api/v1/exports/", map[string]any{})
	var queued struct {
		JobID string `json:"job_id"`
	}
	resp.decode(t, &queued)

	archive := waitForExport(t, c, queued.JobID)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}

	seen := map[string]bool{}
	for _, file := range reader.File {
		if seen[file.Name] {
			t.Fatalf("the archive has two entries named %q", file.Name)
		}
		seen[file.Name] = true
	}
}

func TestExportIsAudited(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	resp := c.post("/api/v1/exports/", map[string]any{"include_pdfs": false, "include_attachments": false})
	var queued struct {
		JobID string `json:"job_id"`
	}
	resp.decode(t, &queued)

	waitForExport(t, c, queued.JobID)

	var log struct {
		Events []struct {
			Operation string `json:"operation"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?entity_type=export&entity_id="+queued.JobID).decode(t, &log)

	operations := map[string]bool{}
	for _, event := range log.Events {
		operations[event.Operation] = true
	}
	for _, want := range []string{"EXPORT_REQUESTED", "EXPORT_BUILT", "EXPORT_DOWNLOADED"} {
		if !operations[want] {
			t.Errorf("audit log is missing %s; recorded: %v", want, keysOf(operations))
		}
	}
}

func TestBackgroundJobsRunAndRetry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A job for a kind nobody handles is a deployment mistake, not a transient
	// failure, so it must fail immediately rather than retry forever.
	var jobID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO background_jobs (kind, payload, max_attempts)
		VALUES ('NO_SUCH_KIND', '{}'::jsonb, 5)
		RETURNING id::text`).Scan(&jobID); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		if err := h.pool.QueryRow(ctx,
			`SELECT state FROM background_jobs WHERE id = $1`, jobID).Scan(&state); err != nil {
			t.Fatalf("read job: %v", err)
		}
		if state == "FAILED" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if state != "FAILED" {
		t.Fatalf("job state = %q after waiting, want FAILED", state)
	}

	var attempts int
	var lastError *string
	if err := h.pool.QueryRow(ctx,
		`SELECT attempts, last_error FROM background_jobs WHERE id = $1`, jobID).
		Scan(&attempts, &lastError); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: an unknown kind must not be retried", attempts)
	}
	if lastError == nil || !strings.Contains(*lastError, "no handler") {
		t.Errorf("last error = %v, want it to name the missing handler", lastError)
	}
}

func TestJobDedupeKeyCollapsesRepeats(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	insert := func() error {
		_, err := h.pool.Exec(ctx, `
			INSERT INTO background_jobs (kind, payload, dedupe_key, run_at)
			VALUES ('SEND_DOCUMENT', '{}'::jsonb, 'same-key', now() + interval '1 hour')`)
		return err
	}

	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert(); err == nil {
		t.Fatal("a second live job with the same dedupe key was accepted")
	}
}

func TestReadOnlyRoleCannotWriteExpensesOrSend(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, owner)
	invoice := issuedInvoice(t, owner, customerID, 10_000)

	const email = "viewer-ops@example.test"
	const password = "viewer-password-here"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "צופה", "password": password, "roles": []string{"READ_ONLY"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create read-only user = %d %s", resp.status, resp.body)
	}

	viewer := h.newClient()
	viewer.loginOK(email, password)

	// Reading is fine, including the reports an accountant needs.
	for _, path := range []string{"/api/v1/expenses/", "/api/v1/reports/dashboard", "/api/v1/reports/unpaid"} {
		if resp := viewer.get(path); resp.status != http.StatusOK {
			t.Errorf("read-only GET %s = %d, want 200", path, resp.status)
		}
	}

	if resp := viewer.post("/api/v1/expenses/", map[string]any{
		"supplier": "ספק", "expense_date": "2026-09-05", "amount_agorot": 100,
	}); resp.status != http.StatusForbidden {
		t.Errorf("read-only expense = %d, want 403", resp.status)
	}
	if resp := viewer.post("/api/v1/documents/"+invoice.ID+"/whatsapp", nil); resp.status != http.StatusForbidden {
		t.Errorf("read-only whatsapp = %d, want 403", resp.status)
	}
	// Exports are the accountant hand-off; a read-only viewer is not part of it.
	if resp := viewer.post("/api/v1/exports/", map[string]any{}); resp.status != http.StatusForbidden {
		t.Errorf("read-only export = %d, want 403", resp.status)
	}
}

// waitForExport polls until a build finishes and returns the downloaded archive.
func waitForExport(t *testing.T, c *client, jobID string) []byte {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		c.get("/api/v1/exports/"+jobID).decode(t, &status)

		switch status.State {
		case "SUCCEEDED":
			download := c.do(http.MethodGet, "/api/v1/exports/"+jobID+"/download", nil, false)
			if download.status != http.StatusOK {
				t.Fatalf("download export = %d %s", download.status, download.body)
			}
			return download.body
		case "FAILED":
			t.Fatalf("the export build failed: %s", status.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("the export did not finish within the deadline")
	return nil
}

func readZipEntry(t *testing.T, reader *zip.Reader, name string) []byte {
	t.Helper()

	file, err := reader.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer file.Close()

	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(file); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return buffer.Bytes()
}

func jerusalem(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	return location
}
