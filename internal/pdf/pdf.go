// Package pdf renders documents to PDF and stores the result permanently.
//
// The pipeline is the one in plan.md 4: a versioned HTML template, rendered by
// headless Chromium (Gotenberg), stored as bytes with a SHA-256 fingerprint.
// A document that cannot be rendered is not issued — an issued document without
// its artifact would break plan.md 3.4.
package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// ErrRendererUnavailable means the renderer could not be reached. Issuance must
// fail loudly rather than produce a document with no PDF.
var ErrRendererUnavailable = errors.New("pdf renderer is unavailable")

// The store that persists rendered PDFs lives in internal/storage: attachments
// are kept the same way, so the two share one implementation.

// Rendered is a finished PDF and everything needed to prove it later.
type Rendered struct {
	Bytes    []byte
	SHA256   string
	ByteSize int64
}

// fingerprint computes the stored hash of the artifact.
func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Renderer turns a complete HTML page into a PDF.
type Renderer interface {
	// Render converts html into PDF bytes. The HTML must be self-contained:
	// the renderer has no network access to fetch stylesheets or fonts.
	Render(ctx context.Context, html string) (*Rendered, error)

	// Name identifies the renderer in logs and health checks.
	Name() string
}

// Gotenberg renders through a Gotenberg service (plan.md 4).
type Gotenberg struct {
	baseURL string
	client  *http.Client
}

// NewGotenberg builds a renderer pointing at a Gotenberg instance.
func NewGotenberg(baseURL string) *Gotenberg {
	return &Gotenberg{
		baseURL: baseURL,
		// Rendering is synchronous inside the issuance transaction, so the
		// timeout is deliberately short: a slow renderer must fail the issuance
		// rather than hold a database transaction open indefinitely.
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name identifies the renderer.
func (g *Gotenberg) Name() string { return "gotenberg" }

// Render posts the HTML to Gotenberg's Chromium route and returns the PDF.
func (g *Gotenberg) Render(ctx context.Context, html string) (*Rendered, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Gotenberg's Chromium route renders the file named index.html.
	part, err := writer.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("build render request: %w", err)
	}
	if _, err := io.WriteString(part, html); err != nil {
		return nil, fmt.Errorf("write html: %w", err)
	}

	// A4 in inches. Margins are deliberately zero here: every document template
	// carries its own `@page { margin: ... }`, and Chromium lets that CSS win over
	// the print settings sent alongside the HTML. Setting a margin here too would
	// be config that silently does nothing — the template owns the page box.
	fields := map[string]string{
		"paperWidth":      "8.27",
		"paperHeight":     "11.7",
		"marginTop":       "0",
		"marginBottom":    "0",
		"marginLeft":      "0",
		"marginRight":     "0",
		"printBackground": "true",
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("write field %s: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	url := g.baseURL + "/forms/chromium/convert/html"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("build render request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRendererUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%w: renderer returned %d: %s",
			ErrRendererUnavailable, resp.StatusCode, bytes.TrimSpace(detail))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rendered pdf: %w", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, fmt.Errorf("renderer returned %d bytes that are not a PDF", len(data))
	}

	return &Rendered{Bytes: data, SHA256: fingerprint(data), ByteSize: int64(len(data))}, nil
}

// Health reports whether the renderer is reachable, for /readyz.
func (g *Gotenberg) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRendererUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: health returned %d", ErrRendererUnavailable, resp.StatusCode)
	}
	return nil
}

// Unavailable is the renderer used when none is configured. It refuses every
// request, so a deployment without a renderer cannot issue documents rather
// than silently issuing them without artifacts.
type Unavailable struct{}

// Name identifies the renderer.
func (Unavailable) Name() string { return "none" }

// Render always fails.
func (Unavailable) Render(context.Context, string) (*Rendered, error) {
	return nil, fmt.Errorf("%w: no PDF renderer is configured (set GOTENBERG_URL)", ErrRendererUnavailable)
}
