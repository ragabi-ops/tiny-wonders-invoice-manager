package tests

import (
	"encoding/json"
	"net/http"
	"testing"
)

// customerFixture is the shape the tests read back from the API.
type customerFixture struct {
	ID                 string `json:"id"`
	CustomerType       string `json:"customer_type"`
	DisplayName        string `json:"display_name"`
	Phone              string `json:"phone"`
	Email              string `json:"email"`
	BusinessOrIDNumber string `json:"business_or_id_number"`
	PreferredDelivery  string `json:"preferred_delivery"`
	Active             bool   `json:"active"`
}

// createCustomer posts a customer and fails the test if it is not created.
func createCustomer(t *testing.T, c *client, body map[string]any) customerFixture {
	t.Helper()
	resp := c.post("/api/v1/customers/", body)
	if resp.status != http.StatusCreated {
		t.Fatalf("create customer = %d %s, want 201", resp.status, resp.body)
	}
	var customer customerFixture
	resp.decode(t, &customer)
	return customer
}

func TestCustomerCreateReadUpdateArchive(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	created := createCustomer(t, c, map[string]any{
		"customer_type": "PERSON",
		"display_name":  "נועה כהן",
		"phone":         "050-123-4567",
		"email":         "Noa@Example.Test",
	})

	if created.DisplayName != "נועה כהן" {
		t.Fatalf("display_name = %q", created.DisplayName)
	}
	// Email is stored lower-cased so lookups and duplicate checks agree.
	if created.Email != "noa@example.test" {
		t.Fatalf("email = %q, want it normalized to lower case", created.Email)
	}
	if created.PreferredDelivery != "NONE" {
		t.Fatalf("preferred_delivery = %q, want the NONE default", created.PreferredDelivery)
	}

	var fetched customerFixture
	c.get("/api/v1/customers/"+created.ID).decode(t, &fetched)
	if fetched.ID != created.ID {
		t.Fatalf("fetched id = %q, want %q", fetched.ID, created.ID)
	}

	resp := c.put("/api/v1/customers/"+created.ID, map[string]any{
		"customer_type":         "BUSINESS",
		"display_name":          "נועה כהן בע״מ",
		"legal_name":            "נועה כהן בע״מ",
		"business_or_id_number": "515123456",
		"phone":                 "050-123-4567",
		"email":                 "noa@example.test",
		"address":               "תל אביב",
		"notes":                 "",
		"preferred_delivery":    "EMAIL",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("update customer = %d %s", resp.status, resp.body)
	}
	var updated customerFixture
	resp.decode(t, &updated)
	if updated.CustomerType != "BUSINESS" || updated.BusinessOrIDNumber != "515123456" {
		t.Fatalf("update did not apply: %+v", updated)
	}

	// Archiving hides a customer without deleting it: issued documents will
	// reference it forever.
	resp = c.put("/api/v1/customers/"+created.ID+"/active", map[string]any{"active": false, "reason": "test"})
	if resp.status != http.StatusOK {
		t.Fatalf("archive customer = %d %s", resp.status, resp.body)
	}

	var listed struct {
		Customers []customerFixture `json:"customers"`
		Total     int               `json:"total"`
	}
	c.get("/api/v1/customers/").decode(t, &listed)
	for _, customer := range listed.Customers {
		if customer.ID == created.ID {
			t.Fatal("an archived customer still appears in the default list")
		}
	}

	c.get("/api/v1/customers/?include_archived=true").decode(t, &listed)
	found := false
	for _, customer := range listed.Customers {
		if customer.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("an archived customer is missing from the archive-inclusive list")
	}
}

func TestDuplicateCustomerIsWarnedAboutButNotBlocked(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	createCustomer(t, c, map[string]any{
		"display_name": "דנה לוי",
		"phone":        "052-777-8888",
	})

	// The same number written differently must still be recognized.
	resp := c.post("/api/v1/customers/", map[string]any{
		"display_name": "דנה ל.",
		"phone":        "0527778888",
	})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate phone = %d %s, want 409", resp.status, resp.body)
	}

	var envelope struct {
		Error struct {
			Details struct {
				Code       string `json:"code"`
				Duplicates []struct {
					Reason   string `json:"reason"`
					Customer struct {
						DisplayName string `json:"display_name"`
					} `json:"customer"`
				} `json:"duplicates"`
			} `json:"details"`
		} `json:"error"`
	}
	resp.decode(t, &envelope)

	if envelope.Error.Details.Code != "DUPLICATE_CUSTOMER" {
		t.Fatalf("details.code = %q, want DUPLICATE_CUSTOMER", envelope.Error.Details.Code)
	}
	if len(envelope.Error.Details.Duplicates) == 0 {
		t.Fatal("the conflict carries no candidates, so the operator cannot judge it")
	}
	if got := envelope.Error.Details.Duplicates[0].Reason; got != "PHONE" {
		t.Fatalf("reason = %q, want PHONE", got)
	}

	// The warning must be overridable: two people really can share a number.
	confirmed := c.post("/api/v1/customers/", map[string]any{
		"display_name":      "דנה ל.",
		"phone":             "0527778888",
		"confirm_duplicate": true,
	})
	if confirmed.status != http.StatusCreated {
		t.Fatalf("confirmed duplicate = %d %s, want 201", confirmed.status, confirmed.body)
	}
}

func TestDuplicateCheckEndpointWritesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	createCustomer(t, c, map[string]any{"display_name": "אבי מזרחי", "email": "avi@example.test"})

	resp := c.post("/api/v1/customers/check-duplicates", map[string]any{
		"display_name": "אבי",
		"email":        "avi@example.test",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("check-duplicates = %d %s, want 200", resp.status, resp.body)
	}

	var payload struct {
		Duplicates []struct {
			Reason string `json:"reason"`
		} `json:"duplicates"`
	}
	resp.decode(t, &payload)
	if len(payload.Duplicates) == 0 || payload.Duplicates[0].Reason != "EMAIL" {
		t.Fatalf("duplicates = %+v, want one EMAIL match", payload.Duplicates)
	}

	var listed struct {
		Total int `json:"total"`
	}
	c.get("/api/v1/customers/").decode(t, &listed)
	if listed.Total != 1 {
		t.Fatalf("customer count = %d after a duplicate check, want 1: the check must not write", listed.Total)
	}
}

func TestCustomerValidationIsHebrewAndPerField(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"missing name", map[string]any{"display_name": "   "}, "display_name"},
		{"bad email", map[string]any{"display_name": "בדיקה", "email": "not-an-email"}, "email"},
		{
			"whatsapp without phone",
			map[string]any{"display_name": "בדיקה", "preferred_delivery": "WHATSAPP"},
			"phone",
		},
		{
			"email delivery without email",
			map[string]any{"display_name": "בדיקה", "preferred_delivery": "EMAIL"},
			"email",
		},
		{
			"unknown type",
			map[string]any{"display_name": "בדיקה", "customer_type": "ROBOT"},
			"customer_type",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := c.post("/api/v1/customers/", testCase.body)
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
				t.Fatalf("message %q for %q is not Hebrew", message, testCase.field)
			}
		})
	}
}

func TestServiceCatalogueRoundTripsMoneyExactly(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	resp := c.post("/api/v1/services/", map[string]any{
		"name":                 "סדנת יצירה",
		"description":          "סדנה לילדים",
		"default_price_agorot": 12_599,
		"unit":                 "שעה",
		"category":             "סדנאות",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create service = %d %s", resp.status, resp.body)
	}

	var created struct {
		ID    string `json:"id"`
		Price int64  `json:"default_price_agorot"`
	}
	resp.decode(t, &created)

	// 125.99 ILS must come back exactly, not 12598 or 12599.0000001.
	if created.Price != 12_599 {
		t.Fatalf("price = %d agorot, want 12599", created.Price)
	}

	// The wire format must be a JSON integer, never a decimal string.
	var raw map[string]json.RawMessage
	resp.decode(t, &raw)
	if string(raw["default_price_agorot"]) != "12599" {
		t.Fatalf("wire value = %s, want the integer 12599", raw["default_price_agorot"])
	}

	if bad := c.post("/api/v1/services/", map[string]any{
		"name": "מחיר שלילי", "default_price_agorot": -1,
	}); bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("negative price = %d %s, want 422", bad.status, bad.body)
	}

	var categories struct {
		Categories []string `json:"categories"`
	}
	c.get("/api/v1/services/categories").decode(t, &categories)
	if len(categories.Categories) != 1 || categories.Categories[0] != "סדנאות" {
		t.Fatalf("categories = %v, want [סדנאות]", categories.Categories)
	}
}

func TestActivityPeriodMustBeOrdered(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	ok := c.post("/api/v1/activities/", map[string]any{
		"name":     "יריד חורף",
		"start_at": "2026-12-01T09:00:00Z",
		"end_at":   "2026-12-01T18:00:00Z",
		"location": "תל אביב",
		"status":   "PLANNED",
	})
	if ok.status != http.StatusCreated {
		t.Fatalf("create activity = %d %s", ok.status, ok.body)
	}

	reversed := c.post("/api/v1/activities/", map[string]any{
		"name":     "הפוך",
		"start_at": "2026-12-02T09:00:00Z",
		"end_at":   "2026-12-01T09:00:00Z",
	})
	if reversed.status != http.StatusUnprocessableEntity {
		t.Fatalf("end before start = %d %s, want 422", reversed.status, reversed.body)
	}

	if unknown := c.post("/api/v1/activities/", map[string]any{
		"name": "סטטוס לא מוכר", "status": "MAYBE",
	}); unknown.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status = %d %s, want 422", unknown.status, unknown.body)
	}
}

func TestActivityWithoutDatesIsAllowed(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// Dates are optional: an activity may be planned before it is scheduled.
	resp := c.post("/api/v1/activities/", map[string]any{"name": "פרויקט פתוח"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create dateless activity = %d %s, want 201", resp.status, resp.body)
	}

	var created struct {
		StartAt *string `json:"start_at"`
		EndAt   *string `json:"end_at"`
		Status  string  `json:"status"`
	}
	resp.decode(t, &created)
	if created.StartAt != nil || created.EndAt != nil {
		t.Fatalf("dates = %v/%v, want both null", created.StartAt, created.EndAt)
	}
	if created.Status != "PLANNED" {
		t.Fatalf("status = %q, want the PLANNED default", created.Status)
	}
}

func TestGlobalSearchSpansEntitiesAndIgnoresShortQueries(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	createCustomer(t, c, map[string]any{"display_name": "נועה כהן", "phone": "050-123-4567"})
	if resp := c.post("/api/v1/services/", map[string]any{
		"name": "סדנת נועה", "default_price_agorot": 5000,
	}); resp.status != http.StatusCreated {
		t.Fatalf("create service = %d %s", resp.status, resp.body)
	}
	if resp := c.post("/api/v1/activities/", map[string]any{"name": "יום הולדת לנועה"}); resp.status != http.StatusCreated {
		t.Fatalf("create activity = %d %s", resp.status, resp.body)
	}

	var results struct {
		Customers  []struct{ Title string } `json:"customers"`
		Services   []struct{ Title string } `json:"services"`
		Activities []struct{ Title string } `json:"activities"`
	}
	c.get("/api/v1/search/?q="+urlQuery("נועה")).decode(t, &results)

	if len(results.Customers) == 0 || len(results.Services) == 0 || len(results.Activities) == 0 {
		t.Fatalf("search missed an entity kind: %+v", results)
	}

	// Searching by phone must find the customer, which is how the operator
	// looks someone up when the phone rings.
	c.get("/api/v1/search/?q="+urlQuery("0501234567")).decode(t, &results)
	if len(results.Customers) != 1 {
		t.Fatalf("phone search returned %d customers, want 1", len(results.Customers))
	}

	// One character must not scan every table.
	c.get("/api/v1/search/?q="+urlQuery("נ")).decode(t, &results)
	if len(results.Customers)+len(results.Services)+len(results.Activities) != 0 {
		t.Fatal("a one-character query returned results; it should be ignored")
	}
}

func TestArchivedRecordsAreHiddenFromGlobalSearch(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	created := createCustomer(t, c, map[string]any{"display_name": "לקוח לארכיון"})
	if resp := c.put("/api/v1/customers/"+created.ID+"/active",
		map[string]any{"active": false, "reason": ""}); resp.status != http.StatusOK {
		t.Fatalf("archive = %d %s", resp.status, resp.body)
	}

	var results struct {
		Customers []struct{ Title string } `json:"customers"`
	}
	c.get("/api/v1/search/?q="+urlQuery("לארכיון")).decode(t, &results)
	if len(results.Customers) != 0 {
		t.Fatalf("archived customer appeared in search: %+v", results.Customers)
	}
}

func TestReadOnlyRoleCannotWriteOperationalData(t *testing.T) {
	h := newHarness(t)
	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const email = "viewer@example.test"
	const password = "viewer-password-here"

	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "צופה",
		"password": password, "roles": []string{"READ_ONLY"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create read-only user = %d %s", resp.status, resp.body)
	}

	viewer := h.newClient()
	viewer.loginOK(email, password)

	// Reading is allowed.
	for _, path := range []string{"/api/v1/customers/", "/api/v1/services/", "/api/v1/activities/"} {
		if resp := viewer.get(path); resp.status != http.StatusOK {
			t.Errorf("read-only GET %s = %d, want 200", path, resp.status)
		}
	}

	// Writing is not.
	writes := []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/customers/", map[string]any{"display_name": "אסור"}},
		{"/api/v1/services/", map[string]any{"name": "אסור"}},
		{"/api/v1/activities/", map[string]any{"name": "אסור"}},
	}
	for _, write := range writes {
		if resp := viewer.post(write.path, write.body); resp.status != http.StatusForbidden {
			t.Errorf("read-only POST %s = %d %s, want 403", write.path, resp.status, resp.body)
		}
	}
}

func TestOperationalChangesAreAudited(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	created := createCustomer(t, c, map[string]any{"display_name": "לקוח מבוקר", "phone": "050-000-0000"})

	if resp := c.put("/api/v1/customers/"+created.ID, map[string]any{
		"customer_type": "PERSON", "display_name": "לקוח מבוקר",
		"legal_name": "", "business_or_id_number": "", "phone": "050-111-1111",
		"email": "", "address": "", "notes": "", "preferred_delivery": "NONE",
	}); resp.status != http.StatusOK {
		t.Fatalf("update = %d %s", resp.status, resp.body)
	}

	var log struct {
		Events []struct {
			Operation     string          `json:"operation"`
			EntityID      *string         `json:"entity_id"`
			BeforeSummary json.RawMessage `json:"before_summary"`
			AfterSummary  json.RawMessage `json:"after_summary"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?entity_type=customer&entity_id="+created.ID).decode(t, &log)

	operations := map[string]bool{}
	for _, event := range log.Events {
		operations[event.Operation] = true
		if event.Operation == "CUSTOMER_UPDATED" {
			if len(event.BeforeSummary) == 0 || len(event.AfterSummary) == 0 {
				t.Error("the update event carries no before/after summary")
			}
		}
	}
	for _, want := range []string{"CUSTOMER_CREATED", "CUSTOMER_UPDATED"} {
		if !operations[want] {
			t.Errorf("audit log is missing %s; recorded: %v", want, operations)
		}
	}
}

func TestCustomerPagingReportsATotal(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	for _, name := range []string{"לקוח א", "לקוח ב", "לקוח ג"} {
		createCustomer(t, c, map[string]any{"display_name": name, "confirm_duplicate": true})
	}

	var page struct {
		Customers []customerFixture `json:"customers"`
		Total     int               `json:"total"`
		Limit     int               `json:"limit"`
		Offset    int               `json:"offset"`
	}
	c.get("/api/v1/customers/?limit=2&offset=0").decode(t, &page)

	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	if len(page.Customers) != 2 || page.Limit != 2 {
		t.Fatalf("page = %d customers with limit %d, want 2 and 2", len(page.Customers), page.Limit)
	}

	c.get("/api/v1/customers/?limit=2&offset=2").decode(t, &page)
	if len(page.Customers) != 1 || page.Offset != 2 {
		t.Fatalf("second page = %d customers at offset %d, want 1 at 2", len(page.Customers), page.Offset)
	}
}
