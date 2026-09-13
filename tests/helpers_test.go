package tests

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
)

func newJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return jar
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// urlQuery escapes a value for use in a query string.
func urlQuery(value string) string { return url.QueryEscape(value) }

// requireRenderer skips a test that needs real PDF rendering when no renderer
// is configured. Issuance genuinely cannot work without one — that is the
// point — so the test is skipped rather than faked.
func requireRenderer(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_GOTENBERG_URL") == "" {
		t.Skip("TEST_GOTENBERG_URL is not set; skipping tests that issue documents")
	}
}

// aCustomer creates a customer and returns its id, for tests that need one but
// do not care about its details.
func (h *harness) aCustomer(t *testing.T, c *client) string {
	t.Helper()
	return createCustomer(t, c, map[string]any{
		"display_name":      "לקוח בדיקה",
		"phone":             "050-000-0001",
		"confirm_duplicate": true,
	}).ID
}

// draftFixture is the slice of a document these tests read back.
type draftFixture struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	FullNumber string `json:"full_number"`
	Total      int64  `json:"total_agorot"`
	Subtotal   int64  `json:"subtotal_agorot"`
	VAT        int64  `json:"vat_agorot"`
	TestMode   bool   `json:"test_mode"`
}

// createDraft posts a transaction-invoice draft and fails the test if it is not
// created.
func createDraft(t *testing.T, c *client, customerID string, lines []map[string]any) draftFixture {
	t.Helper()
	resp := c.post("/api/v1/documents/", map[string]any{
		"document_type": "TRANSACTION_INVOICE",
		"customer_id":   customerID,
		"document_date": "2026-09-07",
		"lines":         lines,
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create draft = %d %s, want 201", resp.status, resp.body)
	}
	var draft draftFixture
	resp.decode(t, &draft)
	return draft
}

// postWithHeaders sends a POST with extra headers, for idempotency keys.
func (c *client) postWithHeaders(path string, body any, headers map[string]string) response {
	c.t.Helper()
	return c.doWithHeaders(http.MethodPost, path, body, true, headers)
}

// doRaw sends a request with a caller-supplied body and content type, for
// multipart uploads.
func (c *client) doRaw(method, path string, body []byte, contentType string) response {
	c.t.Helper()

	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token := c.csrfToken(); token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, body: payload, header: resp.Header}
}

// doRawGet performs a GET and keeps the response headers, for tests that assert
// on Content-Disposition and the like.
func (c *client) doRawGet(path string) response {
	c.t.Helper()

	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, body: payload, header: resp.Header}
}

// aCustomerWithEmail creates a customer that has an email address, for delivery
// tests.
func (h *harness) aCustomerWithEmail(t *testing.T, c *client) string {
	t.Helper()
	return createCustomer(t, c, map[string]any{
		"display_name":      "לקוח עם דוא״ל",
		"email":             "customer@example.test",
		"phone":             "050-000-0002",
		"confirm_duplicate": true,
	}).ID
}

// uploadFile posts a single file as multipart form data under the field "file".
func (c *client) uploadFile(method, path, filename string, content []byte) response {
	c.t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		c.t.Fatalf("build upload: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		c.t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		c.t.Fatalf("close upload: %v", err)
	}

	return c.doRaw(method, path, body.Bytes(), writer.FormDataContentType())
}
