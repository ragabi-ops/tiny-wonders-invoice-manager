// Package templates holds the versioned document templates, embedded into the
// binary. A template version is recorded on every issued document, and an
// existing version is never edited: a historical document must always render
// exactly as it was issued (plan.md 14).
package templates

import "embed"

//go:embed document/*.html
var FS embed.FS

// CurrentDocumentVersion is the template new issuances use. Changing a
// document's appearance means adding a new version here, never editing an
// existing file: documents already issued under v1 must keep rendering exactly
// as they were issued.
//
// v1 — the original invoice, payment request and receipt layout.
// v2 — adds the payment-details block a receipt needs (Phase 4).
// v3 — adds the business logo to the header.
// v4 — makes the watermark and banner data-driven, so a pre-issue preview can
//      be marked as a draft with the same mechanism test mode uses.
const CurrentDocumentVersion = "document/v4"
