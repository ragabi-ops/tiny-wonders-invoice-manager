// Package tests holds integration tests that exercise the real router against a
// real PostgreSQL database.
//
// They run only when TEST_DATABASE_URL is set, so `go test ./...` still works on
// a machine with no database. Point it at a throwaway database: the tests drop
// and recreate the public schema.
//
//	TEST_DATABASE_URL=postgres://invoice:invoice@localhost:5433/invoice_test?sslmode=disable go test ./tests/
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/app"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/config"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/migrations"
)

const (
	ownerEmail    = "owner@example.test"
	ownerPassword = "owner-password-for-tests"
)

// harness is a running API backed by a freshly migrated database.
type harness struct {
	t      *testing.T
	server *httptest.Server
	pool   *db.Pool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return buildHarness(t, os.Getenv("TEST_GOTENBERG_URL"))
}

// newHarnessWithoutRenderer builds an API with no PDF renderer, to prove that
// issuance refuses rather than producing a document with no artifact.
func newHarnessWithoutRenderer(t *testing.T) *harness {
	t.Helper()
	return buildHarness(t, "")
}

func buildHarness(t *testing.T, gotenbergURL string) *harness {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database integration tests")
	}

	// The config package reads the environment, so the test sets it explicitly
	// rather than inheriting a developer's .env.
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("SECURE_COOKIES", "false")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("BOOTSTRAP_OWNER_EMAIL", ownerEmail)
	t.Setenv("BOOTSTRAP_OWNER_PASSWORD", ownerPassword)
	t.Setenv("BOOTSTRAP_OWNER_NAME", "בעלים")
	// Artifacts land in a throwaway directory that the test framework removes.
	t.Setenv("STORAGE_DIR", t.TempDir())
	// Issuance needs a renderer. Tests that exercise it skip when none is set.
	t.Setenv("GOTENBERG_URL", gotenbergURL)
	// Poll fast so a queued job does not add seconds to every test.
	t.Setenv("JOB_POLL_INTERVAL_MS", "100")
	// Backups land in a throwaway directory the test framework removes.
	t.Setenv("BACKUP_DIR", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	// Start from an empty schema so each run is independent.
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		pool.Close()
		t.Fatalf("reset schema: %v", err)
	}

	log := slog.New(slog.DiscardHandler)
	if err := db.Migrate(ctx, pool, migrations.FS, log); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}

	application, err := app.New(cfg, pool, migrations.FS, log)
	if err != nil {
		pool.Close()
		t.Fatalf("build application: %v", err)
	}
	if _, err := application.EnsureBootstrapOwner(ctx); err != nil {
		pool.Close()
		t.Fatalf("bootstrap owner: %v", err)
	}

	// Background work runs in tests too: sending and export building go through
	// the real queue, so the tests exercise the pipeline the deployment uses.
	workerCtx, stopWorker := context.WithCancel(ctx)
	go application.RunWorker(workerCtx)

	server := httptest.NewServer(application.Handler())
	t.Cleanup(func() {
		stopWorker()
		server.Close()
		pool.Close()
	})

	return &harness{t: t, server: server, pool: pool}
}

// client is a browser-like session: it keeps cookies and echoes the CSRF token.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (h *harness) newClient() *client {
	h.t.Helper()
	jar := newJar()
	return &client{
		t:    h.t,
		base: h.server.URL,
		http: &http.Client{Jar: jar, Timeout: 10 * time.Second},
	}
}

type response struct {
	status int
	body   []byte
	header http.Header
}

// decode unmarshals the response body into dst.
func (r response) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("decode response %q: %v", string(r.body), err)
	}
}

// errorCode returns the machine code from the error envelope.
func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	r.decode(t, &envelope)
	return envelope.Error.Code
}

// hebrewMessage returns the user-facing message from the error envelope.
func (r response) hebrewMessage(t *testing.T) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	r.decode(t, &envelope)
	return envelope.Error.Message
}

// do sends a request, attaching the CSRF header for unsafe methods unless
// withCSRF is false — which is how the CSRF guard itself is tested.
func (c *client) do(method, path string, body any, withCSRF bool) response {
	c.t.Helper()
	return c.doWithHeaders(method, path, body, withCSRF, nil)
}

// doWithHeaders is do plus caller-supplied headers.
func (c *client) doWithHeaders(method, path string, body any, withCSRF bool, headers map[string]string) response {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withCSRF && method != http.MethodGet {
		if token := c.csrfToken(); token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
	}
	for name, value := range headers {
		req.Header.Set(name, value)
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

func (c *client) get(path string) response { return c.do(http.MethodGet, path, nil, false) }
func (c *client) post(path string, body any) response {
	return c.do(http.MethodPost, path, body, true)
}
func (c *client) put(path string, body any) response {
	return c.do(http.MethodPut, path, body, true)
}

func (c *client) csrfToken() string {
	for _, cookie := range c.http.Jar.Cookies(mustParse(c.t, c.base)) {
		if cookie.Name == "csrf_token" {
			return cookie.Value
		}
	}
	return ""
}

func (c *client) login(email, password string) response {
	return c.post("/api/v1/auth/login", map[string]string{"email": email, "password": password})
}

func (c *client) loginOK(email, password string) {
	c.t.Helper()
	if resp := c.login(email, password); resp.status != http.StatusOK {
		c.t.Fatalf("login as %s failed: %d %s", email, resp.status, resp.body)
	}
}

// ---------------------------------------------------------------- tests ----

func TestMigrationsApplyAndReadyzPasses(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()

	resp := c.get("/readyz")
	if resp.status != http.StatusOK {
		t.Fatalf("readyz = %d %s, want 200", resp.status, resp.body)
	}

	var payload struct {
		Ready  bool              `json:"ready"`
		Checks map[string]string `json:"checks"`
	}
	resp.decode(t, &payload)
	if !payload.Ready || payload.Checks["migrations"] != "ok" {
		t.Fatalf("readyz reports %+v, want ready with migrations ok", payload)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	h := newHarness(t)

	// Running the migrator again must be a no-op, not a re-application.
	if err := db.Migrate(context.Background(), h.pool, migrations.FS, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second migrate run failed: %v", err)
	}

	var count int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM regulatory_config WHERE key = 'business_mode'`).Scan(&count); err != nil {
		t.Fatalf("count regulatory rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("business_mode has %d rows after two migrate runs, want 1", count)
	}
}

func TestLoginDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	h := newHarness(t)

	unknown := h.newClient().login("nobody@example.test", "some-wrong-password")
	wrongPassword := h.newClient().login(ownerEmail, "some-wrong-password")

	if unknown.status != http.StatusUnauthorized || wrongPassword.status != http.StatusUnauthorized {
		t.Fatalf("statuses %d and %d, want both 401", unknown.status, wrongPassword.status)
	}
	if got, want := unknown.errorCode(t), wrongPassword.errorCode(t); got != want {
		t.Fatalf("codes differ: unknown account %q, wrong password %q", got, want)
	}
	if unknown.hebrewMessage(t) != wrongPassword.hebrewMessage(t) {
		t.Fatal("the two failure messages differ, which lets an attacker enumerate accounts")
	}
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()

	if resp := c.get("/api/v1/auth/me"); resp.status != http.StatusUnauthorized {
		t.Fatalf("me before login = %d, want 401", resp.status)
	}

	c.loginOK(ownerEmail, ownerPassword)

	var me struct {
		User struct {
			Email              string   `json:"email"`
			Roles              []string `json:"roles"`
			MustChangePassword bool     `json:"must_change_password"`
		} `json:"user"`
	}
	c.get("/api/v1/auth/me").decode(t, &me)

	if me.User.Email != ownerEmail {
		t.Fatalf("me returned %q, want %q", me.User.Email, ownerEmail)
	}
	if len(me.User.Roles) != 1 || me.User.Roles[0] != "OWNER" {
		t.Fatalf("bootstrap user roles = %v, want [OWNER]", me.User.Roles)
	}
	if !me.User.MustChangePassword {
		t.Fatal("the bootstrap password travels through the environment, so it must be flagged for replacement")
	}

	if resp := c.post("/api/v1/auth/logout", nil); resp.status != http.StatusNoContent {
		t.Fatalf("logout = %d %s, want 204", resp.status, resp.body)
	}
	// Logout must kill the session server-side, not merely drop the cookie.
	if resp := c.get("/api/v1/auth/me"); resp.status != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", resp.status)
	}
}

func TestUnsafeRequestWithoutCSRFTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	body := map[string]any{
		"email": "csrf@example.test", "display_name": "בדיקה",
		"password": "another-fine-password", "roles": []string{"OPERATOR"},
	}

	withoutToken := c.do(http.MethodPost, "/api/v1/users/", body, false)
	if withoutToken.status != http.StatusForbidden {
		t.Fatalf("POST without CSRF token = %d %s, want 403", withoutToken.status, withoutToken.body)
	}
	if code := withoutToken.errorCode(t); code != "CSRF_TOKEN_INVALID" {
		t.Fatalf("error code = %q, want CSRF_TOKEN_INVALID", code)
	}

	// The same request with the token must succeed, proving the rejection was
	// about CSRF and not about the payload.
	withToken := c.post("/api/v1/users/", body)
	if withToken.status != http.StatusCreated {
		t.Fatalf("POST with CSRF token = %d %s, want 201", withToken.status, withToken.body)
	}
}

func TestRolesAreEnforcedByTheServer(t *testing.T) {
	h := newHarness(t)
	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const operatorEmail = "operator@example.test"
	const operatorPassword = "operator-password-here"

	created := owner.post("/api/v1/users/", map[string]any{
		"email": operatorEmail, "display_name": "מפעיל",
		"password": operatorPassword, "roles": []string{"OPERATOR"},
	})
	if created.status != http.StatusCreated {
		t.Fatalf("create operator = %d %s", created.status, created.body)
	}

	operator := h.newClient()
	operator.loginOK(operatorEmail, operatorPassword)

	// OWNER-only routes must refuse an operator.
	for _, path := range []string{"/api/v1/users/", "/api/v1/audit/", "/api/v1/settings/regulatory"} {
		if resp := operator.get(path); resp.status != http.StatusForbidden {
			t.Errorf("operator GET %s = %d, want 403", path, resp.status)
		}
	}

	// Routes every role may read must still work.
	for _, path := range []string{"/api/v1/compliance/", "/api/v1/settings/business-profile"} {
		if resp := operator.get(path); resp.status != http.StatusOK {
			t.Errorf("operator GET %s = %d, want 200", path, resp.status)
		}
	}
}

func TestDeactivatingAUserEndsTheirSessionImmediately(t *testing.T) {
	h := newHarness(t)
	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	const email = "temporary@example.test"
	const password = "temporary-password-x"

	var created struct {
		ID string `json:"id"`
	}
	resp := owner.post("/api/v1/users/", map[string]any{
		"email": email, "display_name": "זמני",
		"password": password, "roles": []string{"OPERATOR"},
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create user = %d %s", resp.status, resp.body)
	}
	resp.decode(t, &created)

	victim := h.newClient()
	victim.loginOK(email, password)
	if resp := victim.get("/api/v1/compliance/"); resp.status != http.StatusOK {
		t.Fatalf("active user request = %d, want 200", resp.status)
	}

	if resp := owner.put("/api/v1/users/"+created.ID+"/active",
		map[string]any{"active": false, "reason": "test"}); resp.status != http.StatusOK {
		t.Fatalf("deactivate = %d %s", resp.status, resp.body)
	}

	// Access must stop now, not when the session would have expired.
	if resp := victim.get("/api/v1/compliance/"); resp.status != http.StatusUnauthorized {
		t.Fatalf("deactivated user request = %d, want 401", resp.status)
	}
}

func TestTheLastOwnerCannotBeStrippedOfTheRole(t *testing.T) {
	h := newHarness(t)
	owner := h.newClient()
	owner.loginOK(ownerEmail, ownerPassword)

	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	owner.get("/api/v1/auth/me").decode(t, &me)

	resp := owner.put("/api/v1/users/"+me.User.ID+"/roles", map[string]any{"roles": []string{"READ_ONLY"}})
	if resp.status != http.StatusConflict {
		t.Fatalf("demoting the last owner = %d %s, want 409", resp.status, resp.body)
	}
}

func TestComplianceGateBlocksRealIssuanceUntilCleared(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	var status struct {
		Mode                string   `json:"mode"`
		VATApplies          bool     `json:"vat_applies"`
		AllowedDocTypes     []string `json:"allowed_document_types"`
		RealIssuanceEnabled bool     `json:"real_issuance_enabled"`
		Unresolved          []string `json:"unresolved_gate_items"`
		ThresholdAgorot     int64    `json:"threshold_agorot"`
	}
	c.get("/api/v1/compliance/").decode(t, &status)

	if status.Mode != "EXEMPT_DEALER" {
		t.Fatalf("mode = %q, want EXEMPT_DEALER", status.Mode)
	}
	if status.VATApplies {
		t.Fatal("an exempt dealer must never charge VAT")
	}
	if status.RealIssuanceEnabled {
		t.Fatal("real issuance must stay blocked until the compliance gate is cleared")
	}
	if len(status.Unresolved) == 0 {
		t.Fatal("the gate reports no open items, but nothing has been cleared")
	}
	for _, docType := range status.AllowedDocTypes {
		if docType == "TAX_INVOICE" {
			t.Fatal("TAX_INVOICE must never be an allowed type in exempt-dealer mode")
		}
	}
	// 122,833 ILS for 2026, seeded as data rather than hard-coded (plan.md 2).
	if status.ThresholdAgorot != 12_283_300 {
		t.Fatalf("2026 threshold = %d agorot, want 12283300", status.ThresholdAgorot)
	}
}

func TestAuditEventsAreWrittenAndCannotBeMutated(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	var log struct {
		Events []struct {
			ID        int64  `json:"id"`
			Operation string `json:"operation"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?limit=50").decode(t, &log)

	operations := map[string]bool{}
	for _, event := range log.Events {
		operations[event.Operation] = true
	}
	for _, want := range []string{"AUTH_LOGIN_SUCCEEDED", "USER_CREATED"} {
		if !operations[want] {
			t.Errorf("audit log is missing a %s event; recorded: %v", want, operations)
		}
	}

	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `UPDATE audit_events SET operation = 'TAMPERED'`); err == nil {
		t.Fatal("audit_events accepted an UPDATE; the append-only guarantee is broken")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit_events accepted a DELETE; the append-only guarantee is broken")
	}
}

func TestRegulatoryHistoryIsAppendOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx,
		`UPDATE regulatory_config SET value = '"AUTHORIZED_DEALER"' WHERE key = 'business_mode'`); err == nil {
		t.Fatal("regulatory_config accepted an UPDATE; regulatory history must not be rewritten")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM regulatory_config WHERE key = 'business_mode'`); err == nil {
		t.Fatal("regulatory_config accepted a DELETE; regulatory history must not be erased")
	}
}

func TestSettingsChangeIsAuditedWithBeforeAndAfter(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	profile := map[string]any{
		"legal_name": "טיני וונדרס", "display_name": "Tiny Wonders",
		"business_number": "123456789", "address": "תל אביב", "phone": "050-0000000",
		"email": "hello@example.test", "website": "", "bank_details": "",
		"footer_note": "עוסק פטור",
	}
	if resp := c.put("/api/v1/settings/business-profile", profile); resp.status != http.StatusOK {
		t.Fatalf("update business profile = %d %s", resp.status, resp.body)
	}

	var log struct {
		Events []struct {
			Operation     string          `json:"operation"`
			BeforeSummary json.RawMessage `json:"before_summary"`
			AfterSummary  json.RawMessage `json:"after_summary"`
		} `json:"events"`
	}
	c.get("/api/v1/audit/?operation=SETTINGS_UPDATED").decode(t, &log)

	if len(log.Events) == 0 {
		t.Fatal("changing the business profile recorded no audit event")
	}
	event := log.Events[0]
	if len(event.BeforeSummary) == 0 || len(event.AfterSummary) == 0 {
		t.Fatal("the settings audit event lacks a before or after summary")
	}
	if !strings.Contains(string(event.AfterSummary), "טיני וונדרס") {
		t.Fatalf("after_summary does not contain the new value: %s", event.AfterSummary)
	}
}

func TestErrorEnvelopeIsHebrewAndCarriesARequestID(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()

	resp := c.get("/api/v1/auth/me")
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.status)
	}

	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	resp.decode(t, &envelope)

	if envelope.Error.Code != "UNAUTHORIZED" {
		t.Fatalf("code = %q, want UNAUTHORIZED", envelope.Error.Code)
	}
	if envelope.Error.RequestID == "" {
		t.Fatal("the error envelope carries no request_id, so a user cannot quote one to support")
	}
	if !containsHebrew(envelope.Error.Message) {
		t.Fatalf("message %q is not Hebrew; every user-facing string must be", envelope.Error.Message)
	}
}

func TestUnknownJSONFieldsAreRejected(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()
	c.loginOK(ownerEmail, ownerPassword)

	// A client must not be able to send a field the server silently ignores.
	resp := c.post("/api/v1/users/", map[string]any{
		"email": "sneaky@example.test", "display_name": "בדיקה",
		"password": "yet-another-password", "roles": []string{"OPERATOR"},
		"is_superuser": true,
	})
	if resp.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s, want 400", resp.status, resp.body)
	}
}

// containsHebrew reports whether s holds at least one Hebrew letter.
func containsHebrew(s string) bool {
	for _, r := range s {
		if r >= 0x0590 && r <= 0x05FF {
			return true
		}
	}
	return false
}
