# Invoice & Receipt System — Implementation Plan

## 1. Objective

Build a production-ready **internal web application** for a small Israeli business, initially operating as an **`עוסק פטור`**.

The system replaces a basic invoicing service and covers:

- customers
- transaction invoices / payment requests
- payments and receipts
- expenses
- PDF generation and delivery
- reports and accountant exports
- audit trail and backups

### Deployment context (amended 2026-09-08)

This system runs **locally on a single private computer** protected by biometric
access, operated by one person. It is not exposed to the internet and is not
multi-tenant.

Several requirements below are relaxed on that basis; each relaxation is marked
`AMENDED 2026-09-08` and states what must change if the context ever does.

### Mandatory product requirements

- End-user UI: **Hebrew only**
- Full **RTL**
- Currency: **ILS / ₪**
- Timezone: **Asia/Jerusalem**
- Mobile-first responsive UI
- Technical code/API/docs: English
- Official document issuance: server-side and online only

---

## 2. Compliance Boundary

Initial mode: `EXEMPT_DEALER`.

In this mode:

- never issue `חשבונית מס`
- support `חשבונית עסקה` and/or `דרישת תשלום`
- issue `קבלה` when payment is received
- display `עוסק פטור` where required
- do not add VAT to sales
- track annual turnover against a configurable yearly threshold

For 2026, the published exempt-dealer threshold is **₪122,833**. Store regulatory values by effective year/date; never hard-code them permanently.

### Production compliance gate

Before issuing real documents, a tax adviser/accountant must validate:

1. required fields/wording per document
2. receipt timing and payment-detail requirements
3. cancellation/correction behavior
4. computerized-document/signature requirements
5. legal retention requirements
6. whether this implementation requires software registration
7. accountant/export requirements

The Tax Authority generally requires registration for accounting software intended for third-party use and separately describes an internal-development exception. Because AI/external assistance may affect applicability, **do not assume the exception applies**.

Unknown legal behavior must be isolated behind configuration/adapters, not guessed in business logic.

### Future mode

Architect for future `AUTHORIZED_DEALER`, but keep disabled in v1:

- VAT
- tax invoices
- credit documents
- Israel Invoice allocation numbers
- VAT/accounting exports

---

## 3. Core Design Rules

1. **Issued documents are immutable.** No edit/delete after issuance.
2. **Server is authoritative.** Numbering, totals, issue time and state transitions are server-generated.
3. **Numbering is atomic.** No duplicate official numbers, including concurrent requests.
4. **Historical artifacts are preserved.** Store exact PDF + source snapshot + template version + hash.
5. **Financial actions are audited.** Who, what, when and why.
6. **Money uses exact arithmetic.** Integer agorot or PostgreSQL `NUMERIC`, never float.
7. **Regulation is configuration.** Thresholds/rates/rules are effective-dated.
8. **Keep infrastructure simple.** Modular monolith, no distributed systems without need.

---

## 4. Technology Stack

### Backend

- Go, current stable version
- `net/http` + `chi`
- PostgreSQL + `pgx`
- SQL migrations
- explicit SQL, no heavy ORM
- OpenAPI 3.1
- structured JSON logging

### Frontend

- React + TypeScript + Vite
- TanStack Query
- React Router or TanStack Router
- React Hook Form + Zod
- RTL-capable component library

### Documents

```text
Versioned HTML template
-> Chromium/Gotenberg
-> compliance/signature adapter
-> immutable PDF
-> object storage
```

### Deployment

Docker Compose initially:

```text
HTTPS reverse proxy
├── Web
├── Go API
├── PostgreSQL
├── Object storage
└── PDF renderer
```

Do not add Kubernetes, Kafka, Redis or RabbitMQ in v1.

---

## 5. Repository

```text
/
├── apps/api
├── apps/web
├── internal/
│   ├── auth
│   ├── customers
│   ├── catalog
│   ├── documents
│   ├── payments
│   ├── expenses
│   ├── reports
│   ├── delivery
│   ├── audit
│   └── compliance
├── migrations
├── templates
├── deployments
├── docs
├── tests
├── plan.md
├── ARCHITECTURE.md
├── SECURITY.md
└── README.md
```

---

## 6. Users and Security

Roles:

| Role | Scope |
|---|---|
| `OWNER` | Full access, settings, users, audit, backups |
| `OPERATOR` | Customers, documents, payments, expenses, delivery |
| `ACCOUNTANT` | Read, reports, exports, attachments |
| `READ_ONLY` | View only |

Security minimum:

- Argon2id passwords
- secure HTTP-only sessions
- CSRF protection
- rate limiting
- session expiration
- HTTPS production-only
- secrets outside source control
- least-privilege DB/storage credentials
- upload validation
- audit login/security events
- no sensitive data in logs

~~Recommended: TOTP MFA for `OWNER`.~~

**AMENDED 2026-09-08 — not implemented.** The system runs on a private,
biometrically locked machine with no network exposure, so the device itself is
the second factor. Adding TOTP would add a recoverable-secret problem without
addressing a reachable threat.

*Reinstate if:* the system becomes reachable from another machine, or a second
person gets an account.

---

## 7. Hebrew UX

Main navigation:

```text
ראשי
מסמכים
לקוחות
תשלומים
הוצאות
דוחות
פעילויות
ייצוא לרו״ח
הגדרות
```

Primary action: `+ מסמך חדש`.

Requirements:

- `<html lang="he" dir="rtl">`
- Hebrew validation/errors
- Hebrew date/currency formatting
- correct RTL tables/dialogs/icons
- fast global search
- minimal mandatory fields
- large mobile touch targets
- issued documents never show generic Edit/Delete actions

Dashboard should show only useful information:

- revenue today/month/year
- unpaid requests
- recent payments/documents
- expenses this month
- failed delivery jobs
- exempt-dealer annual turnover progress

---

## 8. Customers, Services and Activities

### Customers

Core fields:

```text
id
customer_type
display_name
legal_name
business_or_id_number
phone
email
address
notes
active
```

Features:

- search by name/phone/email/business number
- duplicate warning
- documents/payments timeline
- open balance
- preferred delivery method
- quick inline customer creation

Do not require ID numbers unless needed.

### Services

Reusable catalog items:

```text
name
description
default_price
unit
category
active
```

Allow free-form lines too.

### Activities

Optional operational dimension:

```text
name
start_at
end_at
location
status
notes
```

Documents/payments/expenses may reference an activity for event profitability reporting. Do not build booking/ticketing in v1.

---

## 9. Documents

### v1 types

```text
TRANSACTION_INVOICE   # חשבונית עסקה
PAYMENT_REQUEST       # optional distinct business document
RECEIPT               # קבלה
```

If compliance review determines payment request is only a presentation variant, model it that way instead of duplicating accounting logic.

### States

```text
DRAFT
ISSUED
CANCELLED
```

Drafts are editable and have no official number.

Issuance must atomically:

1. validate current state
2. allocate official number
3. calculate/validate totals server-side
4. snapshot business/customer/document data
5. set server issue timestamp
6. generate final PDF
7. store PDF/hash/template version
8. create audit event
9. commit transaction

Issued documents cannot be edited/deleted.

Cancellation preserves the original number/artifact and records reason, actor, timestamp and corrective relationship. Final correction rules come from the compliance gate.

---

## 10. Numbering and Idempotency

Maintain a sequence per official document type/series.

```text
document_sequences
- document_type
- series
- next_number
```

Sequence allocation must occur inside the same DB transaction as issuance using row locking or equivalent safe PostgreSQL semantics.

Required guarantees:

- no duplicate numbers
- retry-safe issuance
- browser never chooses next number
- concurrency tests must pass before moving on

Critical POST operations accept an idempotency key.

---

## 11. Immutable Snapshots

Every issued document stores the exact values used at issuance:

- business identity/contact details
- customer details
- document lines
- quantities/prices/totals
- payment details when relevant
- required wording
- template version

Never reconstruct a historical issued document from mutable customer/business tables.

---

## 12. Payments and Receipts

Payment methods:

```text
CASH
BANK_TRANSFER
CREDIT_CARD
BIT
PAYBOX
CHECK
OTHER
```

Core payment fields:

```text
id
customer_id
amount
received_at
method
reference
notes
created_by
```

Support:

- full payment
- partial payment
- split/multiple payments
- receipt without prior invoice/request

Primary flow:

```text
Record payment
-> validate
-> persist payment
-> allocate receipt number atomically
-> create immutable receipt
-> render/store PDF
-> audit
-> commit
-> queue delivery
```

Payment + receipt creation must be transactionally consistent and idempotent.

---

## 13. Expenses

Expense tracking is for business/accountant support, not full double-entry bookkeeping.

Store:

```text
supplier
expense_date
amount
category
payment_method
reference
notes
activity_id
attachment
```

Support image/PDF attachments.

Do not implement VAT-input deduction logic for exempt-dealer mode.

OCR can be added later and must always require user confirmation.

---

## 14. PDFs and Delivery

### PDF rules

- Hebrew RTL must render correctly
- store exact issued PDF permanently according to validated retention policy
- store SHA-256 hash
- version templates
- never regenerate historical documents with a new template

Exact legal wording/signature behavior belongs in `DocumentComplianceService` and is enabled only after validation.

### Email

- Hebrew subject/body
- PDF attachment
- delivery status/history
- retry temporary failures

### WhatsApp

v1:

- user-initiated share/deep link
- prepared Hebrew message
- mobile PDF share/download

Future: WhatsApp Business API.

Delivery failure must never roll back a successfully issued document.

---

## 15. Reports and Accountant Export

Required reports:

- revenue by date/customer/service/activity
- payments by method
- unpaid requests
- expenses by category/date/activity
- annual exempt-dealer turnover
- document status/sequence report

Initial accountant export:

```text
export-YYYY-MM-DD.zip
├── documents.csv
├── payments.csv
├── expenses.csv
├── customers.csv
├── pdf/
└── expense-attachments/
```

Use UTF-8 Hebrew-safe CSV.

If required, add the Tax Authority uniform-file format as a dedicated validated export module.

---

## 16. Audit, Configuration and Data Model

### Audit

Append-only audit events for:

- security/login events
- document issuance/cancellation
- payment/receipt actions
- delivery attempts
- settings/regulatory changes
- user/role changes
- exports
- backup/restore actions

Core audit fields:

```text
actor
operation
entity_type
entity_id
timestamp
request_id
before_summary
after_summary
reason
```

### Regulatory configuration

Effective-dated values such as:

```text
business_mode
exempt_turnover_threshold
vat_rates
israel_invoice_threshold
computerized_document_settings
```

### Core tables

```text
users
user_roles
customers
services
activities

documents
document_lines
document_sequences
document_relations

payments
payment_allocations

expenses
attachments

delivery_attempts
audit_events
regulatory_config
background_jobs
```

Use foreign keys, unique constraints and DB checks where appropriate. Store timestamps in UTC and interpret business dates in `Asia/Jerusalem`.

---

## 17. API and Jobs

REST JSON under `/api/v1`.

Resources:

```text
/auth
/customers
/services
/activities
/documents
/payments
/expenses
/reports
/exports
/audit
/settings
```

Explicit financial commands:

```text
POST /documents/{id}/issue
POST /documents/{id}/cancel
POST /payments
POST /receipts/from-payment
POST /documents/{id}/send
```

Rules:

- server validates totals/state/permissions
- no endpoint deletes issued documents
- critical commands support idempotency
- maintain OpenAPI spec

Use PostgreSQL-backed background jobs for email, exports, retries, reminders and backups. **Official issuance stays synchronous.**

---

## 18. Backups and Observability

Back up:

- PostgreSQL
- issued PDFs
- attachments
- configuration

Requirements:

- ~~encrypted backup~~ **AMENDED 2026-09-08:** plain `tar.gz`. On a
  single-operator, biometrically locked machine, a lost key is a larger real
  risk than a readable archive — an encrypted backup that cannot be decrypted is
  not a backup. The folder the archive is synced to is therefore the security
  boundary and must be an account with a strong password and MFA.
  *Reinstate if:* the sync destination becomes shared or less trusted.
- off-machine/off-site copy — the operator's own sync tool uploads `BACKUP_DIR`;
  the application writes the folder and does not upload
- automated schedule — daily at `BACKUP_AT`, catching up after downtime
- documented restore — `docs/RUNBOOK.md`
- periodic restore test — automated in `tests/backup_integration_test.go`;
  quarterly manual drill per the runbook
- alert on backup failure — `/readyz` reports `backups: stale`, the dashboard
  shows it, and every run is recorded in `backup_runs` and the audit log

Legal retention and backup rotation are separate; never purge issued financial records merely because old backups rotate out.

Observability:

- `/healthz`, `/readyz`
- request IDs
- structured logs
- failed-job count
- DB/storage/PDF health
- last successful backup

---

## 19. Required Tests

Automate at minimum:

1. money calculations
2. document state transitions
3. exempt-dealer VAT restrictions
4. sequence allocation
5. concurrent issuance/no duplicate numbers
6. idempotent payment/receipt creation
7. full/partial/split payments
8. RBAC
9. issued-document immutability
10. Hebrew RTL PDF rendering
11. delivery failure does not rollback issuance
12. backup restore smoke test

Critical E2E:

```text
login
-> create customer
-> create and issue transaction invoice/request
-> record payment
-> issue receipt
-> verify Hebrew PDF
-> send/share
-> find documents later
-> verify report/export
```

---

## 20. Implementation Order

### Phase 1 — Foundation

- repo + Docker Compose
- PostgreSQL/migrations
- Go API
- Hebrew RTL React shell
- authentication/RBAC
- settings

### Phase 2 — Core data

- customers
- services
- activities
- search

### Phase 3 — Documents

- drafts
- totals
- sequence engine
- atomic issuance
- snapshots
- PDF generation/storage

**Do not continue until concurrent numbering tests pass.**

### Phase 4 — Payments/receipts

- payment allocation
- partial/split payments
- receipt issuance
- idempotency

### Phase 5 — Operations

- email/WhatsApp share
- expenses/attachments
- dashboard/reports
- accountant export

### Phase 6 — Production hardening

- audit
- MFA
- backups/restore
- observability
- security review
- PDF regression tests

### Phase 7 — Compliance/go-live

Resolve and document all compliance-gate items before enabling real issuance.

---

## 21. Non-Goals for v1

Do not build unless separately requested:

- public multi-tenant SaaS
- payroll
- double-entry accounting engine
- inventory/warehouse
- booking/ticketing
- bank scraping
- payment gateway
- OCR automation
- full CRM
- native mobile apps
- Kubernetes
- authorized-dealer VAT features

---

## 22. Claude Code Instructions

Treat this file as the product/architecture baseline.

Before coding, create only the supporting docs that add value:

- `ARCHITECTURE.md`
- `SECURITY.md`
- `README.md`
- migrations
- OpenAPI spec

Rules:

1. Work phase by phase.
2. Keep the implementation simple and maintainable.
3. Do not invent Israeli legal/accounting rules.
4. Keep unresolved compliance logic behind adapters/configuration.
5. Never weaken numbering, immutability or auditability for UX convenience.
6. Add tests with each critical financial workflow.
7. All user-facing text must be Hebrew/RTL.
8. Code and technical identifiers remain English.
9. Do not add infrastructure without a demonstrated need.

### v1 completion target

```text
create customer
-> issue transaction invoice/payment request
-> record full/partial payment
-> issue immutable sequential receipt
-> generate Hebrew RTL PDF
-> deliver/share
-> audit
-> search later
-> report/export
-> restore from backup
```

Production use starts only after the compliance gate passes.

---

## 23. Official References to Re-check Before Go-Live

Requirements change over time; verify current versions before production:

- Software registry:  
  https://www.gov.il/he/service/itc-software-registry-for-computerized-accounting-systems
- Software registration:  
  https://www.gov.il/he/service/registration-software-designed-managing-computerized-accounting-system
- Exempt dealer / current threshold:  
  https://www.gov.il/he/service/request-open-exempt-dealer-via-internet
- Israel Invoice allocation numbers:  
  https://www.gov.il/he/service/request-assignment-number-for-tax-invoice

---

## Final Direction

Build a **small, reliable financial-document system**, not an ERP.

Protect these guarantees above everything else:

1. no duplicate official numbers
2. no mutation/deletion of issued documents
3. payment-to-receipt consistency
4. preserved historical PDFs/snapshots
5. complete audit trail
6. recoverable backups
7. Hebrew-first UX
8. compliance rules isolated from core domain logic
