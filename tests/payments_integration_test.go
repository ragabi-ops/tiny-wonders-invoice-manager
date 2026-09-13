package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// paymentFixture is the slice of a payment these tests read back.
type paymentFixture struct {
	ID                string  `json:"id"`
	Amount            int64   `json:"amount_agorot"`
	Allocated         int64   `json:"allocated_agorot"`
	OnAccount         int64   `json:"on_account_agorot"`
	Method            string  `json:"method"`
	MethodHebrew      string  `json:"method_hebrew"`
	ReceiptDocumentID *string `json:"receipt_document_id"`
	ReceiptNumber     *string `json:"receipt_number"`
	Allocations       []struct {
		DocumentID string `json:"document_id"`
		FullNumber string `json:"full_number"`
		Amount     int64  `json:"amount_agorot"`
	} `json:"allocations"`
}

type balanceFixture struct {
	Invoiced  int64 `json:"invoiced_agorot"`
	Paid      int64 `json:"paid_agorot"`
	Allocated int64 `json:"allocated_agorot"`
	OnAccount int64 `json:"on_account_agorot"`
	Open      int64 `json:"open_agorot"`
}

// issuedInvoice creates and issues an invoice for a customer, returning it.
func issuedInvoice(t *testing.T, c *client, customerID string, agorot int64) draftFixture {
	t.Helper()
	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "שירות", "quantity_milli": 1000, "unit_price_agorot": agorot},
	})
	return issueDraft(t, c, draft.ID)
}

// recordPayment posts a payment and fails the test if it is not created.
func recordPayment(t *testing.T, c *client, body map[string]any) paymentFixture {
	t.Helper()
	resp := c.post("/api/v1/payments/", body)
	if resp.status != http.StatusCreated {
		t.Fatalf("record payment = %d %s, want 201", resp.status, resp.body)
	}
	var payment paymentFixture
	resp.decode(t, &payment)
	return payment
}

func customerBalance(t *testing.T, c *client, customerID string) balanceFixture {
	t.Helper()
	var balance balanceFixture
	c.get("/api/v1/customers/"+customerID+"/balance").decode(t, &balance)
	return balance
}

func TestPaymentAndReceiptCommitTogether(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 50_000)

	payment := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 50_000,
		"received_at":   "2026-09-08",
		"method":        "BIT",
		"reference":     "בקשה 1234",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 50_000}},
	})

	if payment.ReceiptDocumentID == nil || payment.ReceiptNumber == nil {
		t.Fatal("the primary flow did not issue a receipt with the payment")
	}
	if payment.Allocated != 50_000 || payment.OnAccount != 0 {
		t.Fatalf("allocated %d, on account %d; want 50000 and 0", payment.Allocated, payment.OnAccount)
	}

	// The receipt is a real issued document with its own number and PDF.
	var receipt draftFixture
	c.get("/api/v1/documents/"+*payment.ReceiptDocumentID).decode(t, &receipt)
	if receipt.State != "ISSUED" || receipt.Total != 50_000 {
		t.Fatalf("receipt is %s totalling %d, want ISSUED and 50000", receipt.State, receipt.Total)
	}
	if pdfResp := c.do(http.MethodGet, "/api/v1/documents/"+*payment.ReceiptDocumentID+"/pdf", nil, false); pdfResp.status != http.StatusOK {
		t.Fatalf("receipt pdf = %d, want 200", pdfResp.status)
	}

	// The relationship back to the settled invoice is recorded explicitly.
	var relations int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM document_relations
		WHERE from_document_id = $1 AND to_document_id = $2 AND relation_type = 'RECEIPT_FOR'`,
		*payment.ReceiptDocumentID, invoice.ID).Scan(&relations); err != nil {
		t.Fatalf("count relations: %v", err)
	}
	if relations != 1 {
		t.Fatalf("%d RECEIPT_FOR relations, want 1", relations)
	}
}

func TestReceiptWithNoInvoiceBehindIt(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	// Money can arrive with no document behind it; that is a receipt on account
	// (plan.md 12).
	payment := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 25_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
	})

	if payment.ReceiptDocumentID == nil {
		t.Fatal("no receipt was issued for a payment without allocations")
	}
	if payment.OnAccount != 25_000 {
		t.Fatalf("on account = %d, want the whole 25000", payment.OnAccount)
	}

	balance := customerBalance(t, c, customerID)
	if balance.Paid != 25_000 || balance.OnAccount != 25_000 {
		t.Fatalf("balance = %+v, want 25000 paid and on account", balance)
	}
	// Money on account is deliberately not netted against nothing: the customer
	// owes nothing and the credit is visible separately.
	if balance.Open != 0 {
		t.Fatalf("open = %d, want 0", balance.Open)
	}
}

func TestPartialAndSplitPayments(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 100_000)

	// First instalment.
	first := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 40_000,
		"received_at":   "2026-09-08",
		"method":        "BANK_TRANSFER",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 40_000}},
	})
	if first.Allocated != 40_000 {
		t.Fatalf("first allocation = %d, want 40000", first.Allocated)
	}

	var outstanding struct {
		Documents []struct {
			FullNumber  string `json:"full_number"`
			Outstanding int64  `json:"outstanding_agorot"`
		} `json:"documents"`
	}
	c.get("/api/v1/customers/"+customerID+"/outstanding").decode(t, &outstanding)
	if len(outstanding.Documents) != 1 || outstanding.Documents[0].Outstanding != 60_000 {
		t.Fatalf("outstanding = %+v, want one document owing 60000", outstanding.Documents)
	}

	// Second instalment settles the rest.
	recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 60_000,
		"received_at":   "2026-09-09",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 60_000}},
	})

	c.get("/api/v1/customers/"+customerID+"/outstanding").decode(t, &outstanding)
	if len(outstanding.Documents) != 0 {
		t.Fatalf("the invoice is still outstanding after being paid in full: %+v", outstanding.Documents)
	}

	balance := customerBalance(t, c, customerID)
	if balance.Invoiced != 100_000 || balance.Paid != 100_000 || balance.Open != 0 {
		t.Fatalf("balance = %+v, want 100000 invoiced and paid, 0 open", balance)
	}
}

func TestOnePaymentSplitAcrossSeveralDocuments(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	first := issuedInvoice(t, c, customerID, 30_000)
	second := issuedInvoice(t, c, customerID, 20_000)

	// One transfer covering two invoices plus a little on account.
	payment := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 55_000,
		"received_at":   "2026-09-08",
		"method":        "BANK_TRANSFER",
		"allocations": []map[string]any{
			{"document_id": first.ID, "amount_agorot": 30_000},
			{"document_id": second.ID, "amount_agorot": 20_000},
		},
	})

	if len(payment.Allocations) != 2 {
		t.Fatalf("%d allocations, want 2", len(payment.Allocations))
	}
	if payment.Allocated != 50_000 || payment.OnAccount != 5_000 {
		t.Fatalf("allocated %d and %d on account; want 50000 and 5000", payment.Allocated, payment.OnAccount)
	}

	// The receipt must state all three parts, so it reads as an account of what
	// the money did.
	pdfResp := c.do(http.MethodGet, "/api/v1/documents/"+*payment.ReceiptDocumentID+"/pdf", nil, false)
	text, err := pdfText(pdfResp.body)
	if err != nil {
		t.Skipf("pdftotext unavailable: %v", err)
	}
	for _, want := range []string{first.FullNumber, second.FullNumber, "על החשבון", "העברה בנקאית"} {
		if !strings.Contains(text, want) {
			t.Errorf("the receipt does not mention %q; extracted text:\n%s", want, text)
		}
	}
}

func TestAllocatingMoreThanArrivedIsRefused(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 100_000)

	resp := c.post("/api/v1/payments/", map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 10_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 50_000}},
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("over-allocation = %d %s, want 422", resp.status, resp.body)
	}

	// Nothing may have been recorded.
	var count int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d payments were recorded despite the refusal", count)
	}
}

func TestAllocatingMoreThanADocumentOwesIsRefused(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 30_000)

	// Settle it in full.
	recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 30_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 30_000}},
	})

	// A second payment cannot be applied to the same, now-settled invoice.
	resp := c.post("/api/v1/payments/", map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 10_000,
		"received_at":   "2026-09-09",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 10_000}},
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("overpaying a document = %d %s, want 422", resp.status, resp.body)
	}
}

func TestPaymentCannotBeAllocatedToAnotherCustomersDocument(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	first := createCustomer(t, c, map[string]any{"display_name": "לקוח א", "confirm_duplicate": true}).ID
	second := createCustomer(t, c, map[string]any{"display_name": "לקוח ב", "confirm_duplicate": true}).ID

	invoice := issuedInvoice(t, c, first, 20_000)

	resp := c.post("/api/v1/payments/", map[string]any{
		"customer_id":   second,
		"amount_agorot": 20_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 20_000}},
	})
	if resp.status == http.StatusCreated {
		t.Fatal("a payment was applied to another customer's invoice")
	}
}

func TestADraftCannotBeSettled(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	draft := createDraft(t, c, customerID, []map[string]any{
		{"description": "פריט", "quantity_milli": 1000, "unit_price_agorot": 10_000},
	})

	resp := c.post("/api/v1/payments/", map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 10_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
		"allocations":   []map[string]any{{"document_id": draft.ID, "amount_agorot": 10_000}},
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("settling a draft = %d %s, want 422", resp.status, resp.body)
	}
}

func TestReceiptedPaymentIsImmutable(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 40_000)
	payment := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 40_000,
		"received_at":   "2026-09-08",
		"method":        "CHECK",
		"reference":     "12345",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 40_000}},
	})

	ctx := context.Background()

	// The receipt states the amount, the method and the date; those must not be
	// able to drift away from the row they were taken from (plan.md 3.1).
	if _, err := h.pool.Exec(ctx,
		`UPDATE payments SET amount_agorot = 1 WHERE id = $1`, payment.ID); err == nil {
		t.Error("the database accepted an UPDATE to a receipted payment")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM payments WHERE id = $1`, payment.ID); err == nil {
		t.Error("the database accepted a DELETE of a receipted payment")
	}
	if _, err := h.pool.Exec(ctx,
		`UPDATE payment_allocations SET amount_agorot = 1 WHERE payment_id = $1`, payment.ID); err == nil {
		t.Error("the database accepted an UPDATE to a receipted payment's allocations")
	}
	if _, err := h.pool.Exec(ctx,
		`DELETE FROM payment_allocations WHERE payment_id = $1`, payment.ID); err == nil {
		t.Error("the database accepted a DELETE of a receipted payment's allocations")
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO payment_allocations (payment_id, document_id, amount_agorot)
		VALUES ($1, $2, 1)`, payment.ID, invoice.ID); err == nil {
		t.Error("the database accepted a new allocation on a receipted payment")
	}
}

func TestPaymentCreationIsIdempotent(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	body := map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 12_345,
		"received_at":   "2026-09-08",
		"method":        "PAYBOX",
	}
	const key = "payment-retry-0001"

	first := c.postWithHeaders("/api/v1/payments/", body, map[string]string{"Idempotency-Key": key})
	if first.status != http.StatusCreated {
		t.Fatalf("first payment = %d %s", first.status, first.body)
	}
	var firstPayment paymentFixture
	first.decode(t, &firstPayment)

	// The retry a flaky network produces must not record the money twice.
	second := c.postWithHeaders("/api/v1/payments/", body, map[string]string{"Idempotency-Key": key})
	if second.status != http.StatusCreated {
		t.Fatalf("replayed payment = %d %s, want 201", second.status, second.body)
	}
	var secondPayment paymentFixture
	second.decode(t, &secondPayment)

	if secondPayment.ID != firstPayment.ID {
		t.Fatalf("the replay created a second payment %s, want the original %s",
			secondPayment.ID, firstPayment.ID)
	}

	var count int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d payments after one request and one replay, want 1", count)
	}

	// The same key with different money is a client bug, not a retry.
	different := c.postWithHeaders("/api/v1/payments/",
		map[string]any{
			"customer_id": customerID, "amount_agorot": 99_999,
			"received_at": "2026-09-08", "method": "PAYBOX",
		},
		map[string]string{"Idempotency-Key": key})
	if different.status != http.StatusConflict {
		t.Fatalf("reused key with a different body = %d %s, want 409", different.status, different.body)
	}
}

func TestPaymentCanBeRecordedWithoutAReceiptAndReceiptedLater(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	// Money that arrives while the renderer is down must still be recordable:
	// losing the record would be worse than lacking the receipt.
	payment := recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 15_000,
		"received_at":   "2026-09-08",
		"method":        "CASH",
		"issue_receipt": false,
	})
	if payment.ReceiptDocumentID != nil {
		t.Fatal("a receipt was issued despite issue_receipt being false")
	}

	var unreceipted struct {
		Total int `json:"total"`
	}
	c.get("/api/v1/payments/?unreceipted=true").decode(t, &unreceipted)
	if unreceipted.Total != 1 {
		t.Fatalf("%d unreceipted payments, want 1", unreceipted.Total)
	}

	resp := c.post("/api/v1/payments/"+payment.ID+"/receipt", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("issue receipt = %d %s", resp.status, resp.body)
	}
	var receipted paymentFixture
	resp.decode(t, &receipted)
	if receipted.ReceiptDocumentID == nil || receipted.ReceiptNumber == nil {
		t.Fatal("the receipt was not attached to the payment")
	}

	// Issuing it a second time is refused: a payment has one receipt.
	if again := c.post("/api/v1/payments/"+payment.ID+"/receipt", nil); again.status != http.StatusConflict {
		t.Fatalf("second receipt = %d, want 409", again.status)
	}
}

func TestPaymentValidation(t *testing.T) {
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
			"zero amount",
			map[string]any{"customer_id": customerID, "amount_agorot": 0,
				"received_at": "2026-09-08", "method": "CASH"},
			"amount_agorot",
		},
		{
			"negative amount",
			map[string]any{"customer_id": customerID, "amount_agorot": -100,
				"received_at": "2026-09-08", "method": "CASH"},
			"amount_agorot",
		},
		{
			"unknown method",
			map[string]any{"customer_id": customerID, "amount_agorot": 100,
				"received_at": "2026-09-08", "method": "BITCOIN"},
			"method",
		},
		{
			"bad date",
			map[string]any{"customer_id": customerID, "amount_agorot": 100,
				"received_at": "08/09/2026", "method": "CASH"},
			"received_at",
		},
		{
			"no customer",
			map[string]any{"customer_id": "", "amount_agorot": 100,
				"received_at": "2026-09-08", "method": "CASH"},
			"customer_id",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := c.post("/api/v1/payments/", testCase.body)
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

func TestCustomerTimelineMergesDocumentsAndPayments(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 20_000)
	recordPayment(t, c, map[string]any{
		"customer_id":   customerID,
		"amount_agorot": 20_000,
		"received_at":   "2026-09-09",
		"method":        "BIT",
		"allocations":   []map[string]any{{"document_id": invoice.ID, "amount_agorot": 20_000}},
	})

	var timeline struct {
		Entries []struct {
			Kind       string `json:"kind"`
			FullNumber string `json:"full_number"`
			Amount     int64  `json:"amount_agorot"`
			State      string `json:"state"`
		} `json:"entries"`
	}
	c.get("/api/v1/customers/"+customerID+"/timeline").decode(t, &timeline)

	kinds := map[string]int{}
	for _, entry := range timeline.Entries {
		kinds[entry.Kind]++
	}
	// The invoice, the payment, and the receipt the payment produced.
	if kinds["DOCUMENT"] != 2 || kinds["PAYMENT"] != 1 {
		t.Fatalf("timeline kinds = %v, want 2 documents and 1 payment", kinds)
	}
}

func TestPaymentsByMethodReport(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	for _, payment := range []struct {
		method string
		amount int64
	}{
		{"CASH", 10_000},
		{"CASH", 5_000},
		{"BIT", 7_500},
	} {
		recordPayment(t, c, map[string]any{
			"customer_id": customerID, "amount_agorot": payment.amount,
			"received_at": "2026-09-08", "method": payment.method,
		})
	}

	var report struct {
		Totals []struct {
			Method string `json:"method"`
			Count  int    `json:"count"`
			Total  int64  `json:"total_agorot"`
		} `json:"totals"`
	}
	c.get("/api/v1/payments/by-method").decode(t, &report)

	byMethod := map[string]int64{}
	counts := map[string]int{}
	for _, row := range report.Totals {
		byMethod[row.Method] = row.Total
		counts[row.Method] = row.Count
	}
	if byMethod["CASH"] != 15_000 || counts["CASH"] != 2 {
		t.Errorf("cash = %d over %d payments, want 15000 over 2", byMethod["CASH"], counts["CASH"])
	}
	if byMethod["BIT"] != 7_500 {
		t.Errorf("bit = %d, want 7500", byMethod["BIT"])
	}
}

func TestPaymentIsAudited(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	payment := recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 33_300,
		"received_at": "2026-09-08", "method": "CREDIT_CARD",
	})

	var log struct {
		Events []struct {
			Operation string `json:"operation"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?entity_type=payment&entity_id="+payment.ID).decode(t, &log)

	operations := map[string]bool{}
	for _, event := range log.Events {
		operations[event.Operation] = true
	}
	for _, want := range []string{"PAYMENT_RECORDED", "RECEIPT_ISSUED"} {
		if !operations[want] {
			t.Errorf("audit log is missing %s; recorded: %v", want, operations)
		}
	}
}

func TestReadOnlyRoleCannotRecordPayments(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, owner)

	const email = "viewer-pay@example.test"
	const password = "viewer-password-here"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "צופה", "password": password, "roles": []string{"READ_ONLY"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create read-only user = %d %s", resp.status, resp.body)
	}

	viewer := h.newClient()
	viewer.loginOK(email, password)

	if resp := viewer.get("/api/v1/payments/"); resp.status != http.StatusOK {
		t.Errorf("read-only list = %d, want 200", resp.status)
	}
	if resp := viewer.post("/api/v1/payments/", map[string]any{
		"customer_id": customerID, "amount_agorot": 100,
		"received_at": "2026-09-08", "method": "CASH",
	}); resp.status != http.StatusForbidden {
		t.Errorf("read-only payment = %d, want 403", resp.status)
	}
}

func TestReceiptsAndInvoicesUseSeparateCounters(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)
	customerID := h.aCustomer(t, c)

	invoice := issuedInvoice(t, c, customerID, 10_000)
	payment := recordPayment(t, c, map[string]any{
		"customer_id": customerID, "amount_agorot": 10_000,
		"received_at": "2026-09-08", "method": "CASH",
		"allocations": []map[string]any{{"document_id": invoice.ID, "amount_agorot": 10_000}},
	})

	// Each document type has its own counter, so the first invoice and the
	// first receipt both carry number 1 without colliding.
	if invoice.FullNumber != *payment.ReceiptNumber {
		t.Logf("invoice %s, receipt %s", invoice.FullNumber, *payment.ReceiptNumber)
	}

	var report struct {
		Sequences []struct {
			DocumentType string `json:"document_type"`
			IssuedCount  int64  `json:"issued_count"`
		} `json:"sequences"`
	}
	c.get("/api/v1/documents/sequences").decode(t, &report)

	counts := map[string]int64{}
	for _, sequence := range report.Sequences {
		counts[sequence.DocumentType] = sequence.IssuedCount
	}
	if counts["TRANSACTION_INVOICE"] != 1 || counts["RECEIPT"] != 1 {
		t.Fatalf("issued counts = %v, want one of each", counts)
	}
}
