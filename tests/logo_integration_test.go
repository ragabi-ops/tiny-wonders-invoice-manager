package tests

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// uploadLogo posts a logo as multipart form data.
func uploadLogo(t *testing.T, c *client, filename string, content []byte) response {
	t.Helper()
	return c.uploadFile(http.MethodPost, "/api/v1/settings/business-profile/logo", filename, content)
}

func TestBusinessLogoUploadAndServe(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// No logo to begin with.
	var profile struct {
		HasLogo bool `json:"has_logo"`
	}
	c.get("/api/v1/settings/business-profile").decode(t, &profile)
	if profile.HasLogo {
		t.Fatal("a fresh business reports a logo it never uploaded")
	}
	if resp := c.get("/api/v1/settings/business-profile/logo"); resp.status != http.StatusNotFound {
		t.Fatalf("logo before upload = %d, want 404", resp.status)
	}

	resp := uploadLogo(t, c, "logo.png", tinyPNG)
	if resp.status != http.StatusOK {
		t.Fatalf("upload logo = %d %s, want 200", resp.status, resp.body)
	}
	resp.decode(t, &profile)
	if !profile.HasLogo {
		t.Fatal("the profile does not report the uploaded logo")
	}

	download := c.doRawGet("/api/v1/settings/business-profile/logo")
	if download.status != http.StatusOK {
		t.Fatalf("download logo = %d, want 200", download.status)
	}
	if !bytes.Equal(download.body, tinyPNG) {
		t.Fatal("the served logo differs from what was uploaded")
	}
	if download.header.Get("Content-Type") != "image/png" {
		t.Errorf("content type = %q, want image/png", download.header.Get("Content-Type"))
	}
	if download.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the logo is served without nosniff")
	}
}

func TestLogoTypeIsDecidedByContentAndSVGIsRefused(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// SVG is a script-capable document that would be embedded into a page the
	// renderer executes, so it is refused however it is named.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if resp := uploadLogo(t, c, "logo.png", svg); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("svg logo = %d %s, want 422", resp.status, resp.body)
	}

	// A PDF is not an image, whatever the filename claims.
	if resp := uploadLogo(t, c, "logo.png", tinyPDF); resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("pdf logo = %d %s, want 422", resp.status, resp.body)
	}

	// A real PNG is accepted under any name.
	if resp := uploadLogo(t, c, "anything.txt", tinyPNG); resp.status != http.StatusOK {
		t.Fatalf("png logo = %d %s, want 200", resp.status, resp.body)
	}
}

func TestChangingTheLogoDoesNotChangeDocumentsAlreadyIssued(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := uploadLogo(t, c, "first.png", tinyPNG); resp.status != http.StatusOK {
		t.Fatalf("upload first logo = %d %s", resp.status, resp.body)
	}

	customerID := h.aCustomer(t, c)
	invoice := issuedInvoice(t, c, customerID, 10_000)

	var firstSnapshot struct {
		Business struct {
			LogoPath   string `json:"logo_path"`
			LogoSHA256 string `json:"logo_sha256"`
		} `json:"business"`
	}
	c.get("/api/v1/documents/"+invoice.ID+"/snapshot").decode(t, &firstSnapshot)

	if firstSnapshot.Business.LogoPath == "" || firstSnapshot.Business.LogoSHA256 == "" {
		t.Fatal("the snapshot does not pin the logo it was issued with")
	}

	// Replace the logo with different bytes.
	secondLogo := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("y"), 128)...)
	if resp := uploadLogo(t, c, "second.jpg", secondLogo); resp.status != http.StatusOK {
		t.Fatalf("upload second logo = %d %s", resp.status, resp.body)
	}

	// The already-issued document must still point at the original file, and
	// that file must still be readable: logo files are never overwritten.
	var afterSnapshot struct {
		Business struct {
			LogoPath   string `json:"logo_path"`
			LogoSHA256 string `json:"logo_sha256"`
		} `json:"business"`
	}
	c.get("/api/v1/documents/"+invoice.ID+"/snapshot").decode(t, &afterSnapshot)

	if afterSnapshot.Business.LogoSHA256 != firstSnapshot.Business.LogoSHA256 {
		t.Fatalf("the snapshot followed the logo change: %s became %s",
			firstSnapshot.Business.LogoSHA256, afterSnapshot.Business.LogoSHA256)
	}
	if afterSnapshot.Business.LogoPath == "" {
		t.Fatal("the snapshot lost its logo path")
	}

	// Its stored PDF is untouched too.
	if resp := c.do(http.MethodGet, "/api/v1/documents/"+invoice.ID+"/pdf", nil, false); resp.status != http.StatusOK {
		t.Fatalf("the issued PDF is no longer readable: %d", resp.status)
	}

	// A document issued now records the new logo.
	next := issuedInvoice(t, c, customerID, 20_000)
	var nextSnapshot struct {
		Business struct {
			LogoSHA256 string `json:"logo_sha256"`
		} `json:"business"`
	}
	c.get("/api/v1/documents/"+next.ID+"/snapshot").decode(t, &nextSnapshot)

	if nextSnapshot.Business.LogoSHA256 == firstSnapshot.Business.LogoSHA256 {
		t.Fatal("a document issued after the change still records the old logo")
	}
}

func TestDocumentsIssueFineWithNoLogo(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// A business that has not uploaded a logo must still be able to issue.
	invoice := issuedInvoice(t, c, h.aCustomer(t, c), 15_000)
	if invoice.State != "ISSUED" {
		t.Fatalf("state = %q, want ISSUED", invoice.State)
	}

	if resp := c.do(http.MethodGet, "/api/v1/documents/"+invoice.ID+"/pdf", nil, false); resp.status != http.StatusOK {
		t.Fatalf("pdf = %d, want 200", resp.status)
	}
}

func TestRemovingTheLogoOnlyAffectsFutureDocuments(t *testing.T) {
	h := newHarness(t)
	requireRenderer(t)

	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := uploadLogo(t, c, "logo.png", tinyPNG); resp.status != http.StatusOK {
		t.Fatalf("upload = %d %s", resp.status, resp.body)
	}

	invoice := issuedInvoice(t, c, h.aCustomer(t, c), 10_000)

	if resp := c.do(http.MethodDelete, "/api/v1/settings/business-profile/logo", nil, true); resp.status != http.StatusOK {
		t.Fatalf("remove logo = %d %s", resp.status, resp.body)
	}

	var profile struct {
		HasLogo bool `json:"has_logo"`
	}
	c.get("/api/v1/settings/business-profile").decode(t, &profile)
	if profile.HasLogo {
		t.Fatal("the profile still reports a logo after removal")
	}

	// The issued document keeps its own.
	var snapshot struct {
		Business struct {
			LogoSHA256 string `json:"logo_sha256"`
		} `json:"business"`
	}
	c.get("/api/v1/documents/"+invoice.ID+"/snapshot").decode(t, &snapshot)
	if snapshot.Business.LogoSHA256 == "" {
		t.Fatal("removing the logo erased it from a document already issued")
	}
}

func TestOnlyOwnerCanChangeTheLogo(t *testing.T) {
	h := newHarness(t)

	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const email = "operator-logo@example.test"
	const password = "operator-password-x"
	if resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "מפעיל", "password": password, "roles": []string{"OPERATOR"},
	}); resp.status != http.StatusCreated {
		t.Fatalf("create operator = %d %s", resp.status, resp.body)
	}

	operator := h.newClient()
	operator.loginOK(email, password)

	// The logo is business identity, so it lives with the other OWNER-only
	// settings; an operator may see it but not change it.
	if resp := uploadLogo(t, operator, "logo.png", tinyPNG); resp.status != http.StatusForbidden {
		t.Errorf("operator upload = %d, want 403", resp.status)
	}
	if resp := operator.get("/api/v1/settings/business-profile"); resp.status != http.StatusOK {
		t.Errorf("operator read profile = %d, want 200", resp.status)
	}
}

func TestSavingTheProfileFormDoesNotClearTheLogo(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	if resp := uploadLogo(t, c, "logo.png", tinyPNG); resp.status != http.StatusOK {
		t.Fatalf("upload = %d %s", resp.status, resp.body)
	}

	// The profile form carries no logo fields, so saving it must leave the logo
	// alone rather than nulling it.
	if resp := c.put("/api/v1/settings/business-profile", map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "טיני וונדרס",
		"business_number": "515123456", "address": "תל אביב", "phone": "050-1112222",
		"email": "hello@example.test", "website": "", "bank_details": "", "footer_note": "",
	}); resp.status != http.StatusOK {
		t.Fatalf("save profile = %d %s", resp.status, resp.body)
	}

	var profile struct {
		HasLogo   bool   `json:"has_logo"`
		LegalName string `json:"legal_name"`
	}
	c.get("/api/v1/settings/business-profile").decode(t, &profile)

	if !profile.HasLogo {
		t.Fatal("saving the business details cleared the logo")
	}
	if !strings.Contains(profile.LegalName, "טיני") {
		t.Fatalf("legal name = %q, want the saved value", profile.LegalName)
	}
}
