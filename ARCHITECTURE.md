# Architecture

Baseline: [`plan.md`](./plan.md). This file records *how* the plan's rules are implemented.

## 1. Shape

Modular monolith. One Go binary, one PostgreSQL database, one React SPA.
No Kubernetes, Kafka, Redis or message broker (plan §4).

```text
Browser (Hebrew RTL SPA)
  │  JSON, cookie session, same-origin
  ▼
Caddy ──┬── /api, /healthz, /readyz ──► Go API ──► PostgreSQL
        │                                 │
        └── everything else ──► SPA        ├─► Artifact store (issued PDFs, attachments)
            bundle from /srv               └─► Gotenberg (HTML → PDF)
```

Caddy is the only web server, and serving the bundle itself rather than proxying
to a second one is deliberate: security headers and cache rules are then stated
in exactly one place. It also makes the SPA same-origin with the API, so no CORS
allow-list is needed and the session cookie stays first-party.

For this deployment Caddy is published on `127.0.0.1` only and terminates TLS
with its own CA (`tls internal`), because the hostname resolves through
`/etc/hosts` and no public authority could issue for it. That is what lets the
API run with `APP_ENV=production` and `SECURE_COOKIES=true`; `config.go` ties
those two together so neither can be raised alone.

## 2. Package layout

`internal/` packages own a domain slice. A package exposes a `Service` (business rules,
owns its transactions) and `Handlers` (HTTP, no business logic). Handlers never touch
`pgxpool` directly; services never write HTTP responses.

| Package | Owns |
|---|---|
| `config` | env parsing, one immutable `Config` |
| `db` | pool, migration runner, `Tx` helper |
| `httpx` | request IDs, JSON encode/decode, error envelope, panic recovery, CORS, rate limit |
| `auth` | passwords, sessions, CSRF, login/logout, RBAC middleware |
| `users` | user records, roles |
| `settings` | effective-dated regulatory configuration |
| `audit` | append-only audit events |
| `compliance` | business-mode rules; isolates unresolved legal behavior |
| `money` | ILS amounts as integer agorot |
| `customers` | customers, and the duplicate warnings on entering one |
| `catalog` | the reusable service catalogue |
| `activities` | events and projects documents can be attributed to |
| `search` | one query across customers, services and activities |
| `documents` | drafts, totals, the sequence engine, issuance, snapshots |
| `pdf` | rendering through Gotenberg, and the artifact store |
| `idempotency` | retry-safety for critical commands |
| `payments` | money received, its allocation, receipts, balances |
| `expenses` | what the business spends, and the receipts attached to it |
| `storage` | immutable files on disk: issued PDFs, attachments, exports |
| `jobs` | the PostgreSQL-backed work queue |
| `delivery` | email and WhatsApp share, and the history of every attempt |
| `reports` | revenue, unpaid, turnover, the dashboard |
| `exports` | the accountant hand-off archive |
| `backup` | the daily archive, its schedule, and the record of every run |

Dependency direction is one-way: `httpx` ← domain packages ← `apps/api`. Domain packages
do not import each other except through narrow interfaces declared by the consumer.

## 3. Money

Money is `int64` **agorot** everywhere: Go (`money.Amount`), PostgreSQL (`BIGINT`), JSON
(integer). Floats never touch a monetary value. Formatting to `₪1,234.56` happens only in
the browser via `Intl.NumberFormat('he-IL')`.

Rationale: exact by construction, no rounding mode to get wrong, no decimal dependency.
`plan.md` §3.6 allows either agorot or `NUMERIC`; agorot was chosen for simplicity.

## 4. Time

All timestamps are `TIMESTAMPTZ` stored in UTC. Business dates (document date, expense
date, report ranges, turnover year) are interpreted in `Asia/Jerusalem`. The conversion
lives in one place: `internal/config.Config.Location()`. `DATE` columns hold already-
localized business dates and are never derived from a UTC timestamp in SQL.

## 5. Server authority

The browser is never trusted for anything financial. The server generates official
numbers, totals, issue timestamps and state transitions (plan §3.2). API responses carry
server-computed values; request bodies carrying totals are ignored, not validated-and-used.

## 6. Immutability

Issued documents are append-only, enforced by a database trigger and not only by
application code. `documents_enforce_immutability()` permits exactly one change to an
`ISSUED` row — the transition to `CANCELLED`, which may set only the cancellation
fields — and rejects every other update, every delete, and any change at all to a
`CANCELLED` row. A second trigger does the same for `document_lines`, including
refusing an insert onto an already-issued document.

Table `CHECK` constraints close the other half: an `ISSUED` row must carry its number,
issue time, snapshot, PDF path, hash and template version, so a row cannot claim to be
issued without its artifacts. Corrections create new rows plus a `document_relations`
edge; nothing is ever rewritten in place.

## 7. Numbering

`document_sequences` holds `next_number` per `(document_type, series)`. Allocation
happens inside the issuance transaction as a single
`UPDATE ... RETURNING next_number - 1`, which takes the row lock: a second transaction
allocating the same counter blocks until the first commits or rolls back. A rolled-back
issuance therefore *returns* its number rather than leaving a gap.

A partial unique index on `(document_type, series, number)` is the belt to that braces —
even SQL that bypasses the allocator cannot land a duplicate official number.

Two series exist. `A` is official. `TEST` is used while the compliance gate is
unresolved, so nothing issued before a tax adviser signs off can consume an official
number; those documents are flagged `test_mode` and watermarked.

The concurrency tests in `tests/numbering_concurrency_test.go` are the gate plan.md §20
puts in front of all later work.

## 8. Issuance

One transaction performs the whole of plan.md §9: validate the state (holding the row),
allocate the number, recompute the totals from the stored lines, snapshot, stamp the
server issue time, render the PDF, store it with its hash, write the audit event,
commit.

Rendering happens *inside* the transaction on purpose: if the renderer is down nothing
commits and no number is consumed. There is no path that produces an issued document
without its PDF.

A line's money is `unit_price × quantity`, where the quantity is integer thousandths
(1.5 hours is `1500`), rounded to whole agorot **half away from zero**, once, per line.
Totals are then exact sums of already-rounded lines, so a document's printed total always
equals the sum of its printed lines.

## 9. Idempotency

`POST /documents/{id}/issue` accepts an `Idempotency-Key`. The first request stores its
response body in `idempotency_keys`, keyed by `(scope, key)` together with a fingerprint
of the request; a replay returns that stored response with `Idempotent-Replay: true`, and
the same key against a *different* request is refused rather than silently answered with
the wrong response.

Without a key, a second issue of an already-issued document is simply rejected — it never
allocates a second number.

## 10. Documents and PDFs

The printable form of a document is built from its **snapshot alone** — never from the
live customer, business or catalogue tables — so re-rendering a document issued last year
produces last year's content even if the customer has since moved. Templates live in
`templates/document/vN.html`, are embedded in the binary, and are never edited once used:
a change means a new version, and each document records the version it was issued under.

Artifacts are written under `STORAGE_DIR`, foldered by issue month, written atomically
(temp file plus rename) and read back only after the SHA-256 recorded on the document is
re-verified. A local volume is enough for a single-instance deployment; object storage
would be infrastructure without a demonstrated need. Backups cover this directory.

## 11. Payments and receipts

A payment is a record in its own right; what it settles is a separate set of
explicit `payment_allocations`. That is what makes a partial payment, a split
payment, one payment across several invoices, and a payment with no invoice
behind it all the same shape rather than four special cases.

`POST /payments` runs the whole of plan.md §12 in one transaction: persist the
payment, record its allocations, allocate the receipt number, issue the receipt
through the same shared code path the draft flow uses, render and store its PDF,
audit, commit. The payment and its receipt therefore can never disagree.

Two limits are checked inside that transaction, because SQL cannot express
either as a constraint: the sum of a payment's allocations may not exceed the
payment, and no allocation may exceed what its document still owes. Each target
document's row is locked while its outstanding amount is read, so two payments
landing on one invoice at the same moment cannot both claim the same balance.

`issue_receipt: false` records the payment alone. It exists for one real
situation — money arriving while the renderer is down — because losing the
record of received money is worse than lacking its receipt for an hour. The
receipt is issued afterwards through `POST /payments/{id}/receipt`.

Once a payment has a receipt, a database trigger freezes the payment row and its
allocations: the receipt's snapshot states the amount, the method and the date,
and those must not be able to drift away from the row they were taken from.

**Open balance** is `invoiced − allocated`. Money on account is reported
separately and deliberately *not* netted off: it has not been applied to
anything, and which document it eventually settles is the operator's decision,
not an accident of arithmetic.

## 12. Delivery

**A failed send must never roll back an issued document** (plan.md 14). That one
rule decides the shape: sending happens *after* issuance has committed, in a
background job, in its own transaction. There is no code path where a delivery
error can reach the issuance transaction, because by then it has long since
ended.

Every attempt — queued, sent or failed — is written to `delivery_attempts`,
which is append-only. A failure the operator cannot see would be worse than one
they can, so failures surface on the dashboard.

Email is optional. With no SMTP configured the mailer reports itself
unconfigured and the UI offers WhatsApp and download instead of a send that
cannot work. Temporary SMTP failures are retried with backoff; STARTTLS is
required, so credentials and customer documents never cross the network in the
clear.

WhatsApp in v1 is deliberately user-initiated: the server prepares a Hebrew
message and a `wa.me` link, and the operator sends it from their own phone. The
Business API, with its templates and approvals, is a later step.

## 13. Background jobs

`background_jobs` is the queue. Workers claim with `FOR UPDATE SKIP LOCKED`, so
two API instances never run the same job and no broker is needed. Retries use
exponential backoff capped at fifteen minutes; a job whose `kind` has no
registered handler fails immediately, because retrying a deployment mistake
would never fix it.

Succeeded jobs are pruned after thirty days. Failures are kept: they are the
record of something that needed attention.

**Document issuance never runs here.** It stays synchronous, because a document
must exist with its number and its PDF by the time the operator sees a response.

## 14. Reports and the export

Revenue counts issued, uncancelled invoices and payment requests. Receipts are
excluded everywhere — a receipt acknowledges money the document it settles
already accounts for, and counting both would double every figure.

The accountant export is one ZIP: four CSVs, a manifest, the issued PDFs under
`pdf/<document_type>/`, and the expense attachments. The type folder is not
decoration: each document type has its own counter, so an invoice and a receipt
can both be number 1 and would otherwise collide on one name.

Every CSV starts with a UTF-8 BOM. Without it Excel on Windows opens Hebrew as
mojibake, which is exactly how the accountant will open it.

## 15. Preview

Issuance is irreversible, so there is one cheap look first. `GET
/documents/{id}/preview` renders a draft exactly as issuance would — same
template, business profile and logo — and returns it inline. It **allocates
nothing and stores nothing**: no number, no snapshot, no saved PDF, and the
draft stays editable.

`POST /payments/preview` does the same for the receipt a payment would produce.
Receipts are never drafts, so this is the only moment one can be inspected
before it becomes immutable. The preview and the real issuance build the receipt
through the same `receiptRequest` function, so what the operator approves is
what gets issued.

Both outputs carry a `טיוטה` watermark and a banner, driven by template data
rather than a hard-coded string — which is why template v4 exists. A printed
preview can therefore never be mistaken for the document.

## 16. Backups

One gzipped tar per day in `BACKUP_DIR`: a `pg_dump` custom-format dump, every
stored file, and a manifest. Exports are excluded — they are rebuilt on demand
from the data being backed up.

The archive is **not encrypted**, a deliberate amendment to plan.md §18: for a
single operator on a locked machine, a lost key is a larger real risk than a
readable archive. The synced folder is therefore the security boundary.

The schedule is a rule, not a cron expression: *if no backup has succeeded since
today's scheduled time, run one*. That survives the two things a local
deployment actually does — the machine being asleep at 02:00, and the process
restarting — without a queue of missed firings to catch up on.

Writes are atomic (temp file, then rename), so a crash never leaves a truncated
archive that looks complete. Pruning bounds the folder, never removes the last
archive, and has nothing to do with records retention: financial records live in
the database and the artifact store and are never pruned.

`pg_dump`'s major version must match the server's. A newer client emits settings
an older server rejects on restore, which makes a backup *look* successful while
its restore is degraded — the worst failure mode a backup has. The service
checks the versions, warns at startup, and reports the mismatch in its status;
the Docker image pins a matching client.

The restore procedure is in `docs/RUNBOOK.md` and is exercised by
`TestRestoreFromBackupSmokeTest`, which restores into a scratch database and
verifies both the data and that the immutability triggers came back with it.

## 17. Migrations

Plain, ordered, forward-only SQL in `migrations/NNNN_name.sql`, embedded in the binary via
`go:embed`. A `schema_migrations` table records applied versions; the runner takes a
PostgreSQL advisory lock so concurrent API instances cannot race. No third-party migration
CLI is required.

Down-migrations are deliberately absent: a financial database is repaired forward.

## 18. Authentication and sessions

Server-side sessions in PostgreSQL. The cookie carries a 32-byte random token; the database
stores only its SHA-256 hash, so a database leak does not yield usable sessions. Passwords
are Argon2id. Details and parameters: [`SECURITY.md`](./SECURITY.md).

RBAC is a middleware over four roles — `OWNER`, `OPERATOR`, `ACCOUNTANT`, `READ_ONLY` —
checked per route. Roles are compared server-side against the session's user row, never
against a client-supplied claim.

## 19. Regulatory configuration

`regulatory_config` is effective-dated: `(key, effective_from, value_json)`. Reads resolve
"the row with the greatest `effective_from` ≤ the business date". Nothing regulatory is a
Go constant. The exempt-dealer threshold for 2026 (`₪122,833`) is seeded as data, not code.

`internal/compliance` exposes the business-mode rules as an adapter. In `EXEMPT_DEALER`:
VAT is never added, `חשבונית מס` can never be issued. Unresolved legal questions from the
compliance gate stay behind this adapter rather than being guessed inside domain logic.

## 20. Audit

`audit_events` is append-only (insert-only grant plus a trigger blocking update/delete).
Every financial and security action writes one event inside the same transaction as the
action itself, so an audited action cannot commit without its trail.

## 21. Errors

One JSON envelope: `{"error": {"code": "...", "message": "...", "details": {...}}}`.
`code` is a stable English machine identifier. `message` is Hebrew and shown to the user.
The web client maps unknown codes to a generic Hebrew message and never prints English.

## 22. Search

Search is `ILIKE '%q%'` against a stored generated `search_text` column on each
searchable table, backed by a `pg_trgm` GIN index. Trigrams, not PostgreSQL's
word-based full-text search, because that has no useful Hebrew stemming and
operators search by fragments — half a name, the tail of a phone number.

A customer's `search_text` carries the phone twice, as typed and as bare digits,
so `050-123-4567` is found by either spelling. `phone_digits` is a separate
generated column used for duplicate detection; a generated column cannot
reference another, hence the repeated expression.

Global search (`/search`) is one `UNION ALL` — a single round trip — capped at a
few hits per kind with no paging. It is a navigation aid; anything needing
filters or totals belongs to that entity's own list endpoint.

## 23. Duplicates and archiving

Duplicate customers are **warned about, never blocked**. Creating one that
matches an existing phone, email, business number or a similar name returns
`409` with the candidates attached; the operator resubmits with
`confirm_duplicate` if it really is a new customer. A unique constraint would
force staff to falsify data to get past it — two family members can share a
phone number.

Nothing operational is deleted. Customers and catalogue items are archived
(`active = false`), because issued documents reference them and must stay
readable for as long as the law requires them kept.

## 24. Frontend

Vite SPA, `<html lang="he" dir="rtl">`, Mantine with `DirectionProvider` for RTL layout.
TanStack Query owns server state; React Hook Form + Zod own form state. There is no client
state library — anything durable belongs to the server.

Issued documents render without generic Edit/Delete affordances (plan §7).
