# Security

Scope: an internal financial-document system holding customer identity data, issued tax
documents and payment records. Threat model is a small trusted staff plus the public
internet reaching the reverse proxy.

## 1. Authentication

**Passwords — Argon2id** (`golang.org/x/crypto/argon2`).

| Parameter | Value |
|---|---|
| memory | 64 MiB |
| iterations | 3 |
| parallelism | 2 |
| salt | 16 random bytes |
| key length | 32 bytes |

Encoded as the standard `$argon2id$v=19$m=...,t=...,p=...$salt$hash` string, so parameters
can be raised later and old hashes still verify. Verification uses a constant-time compare.
Minimum password length is 12 characters; the check is server-side.

Login is rate limited per IP **and** per account. Failed logins return one generic Hebrew
error — never "no such user" versus "wrong password" — so accounts cannot be enumerated.
Both outcomes take comparable time.

**MFA**: deliberately **not implemented**. The system runs on one private,
biometrically locked machine with no network exposure, so the device supplies the
second factor; TOTP would add a recoverable-secret problem without closing a
reachable path. `plan.md` §6 was amended accordingly on 2026-09-08.

Reinstate it the moment the system becomes reachable from another machine, or a
second person gets an account.

## 2. Sessions

Server-side sessions in PostgreSQL.

- Token: 32 bytes from `crypto/rand`, base64url in the cookie.
- The database stores **only** `sha256(token)`. A database dump yields no usable session.
- Cookie: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` whenever `SECURE_COOKIES=true`
  (required in production), no `Domain` attribute.
- Absolute lifetime `SESSION_TTL_HOURS` (default 12) and idle timeout
  `SESSION_IDLE_TIMEOUT_MINUTES` (default 60). Both are enforced server-side; the cookie's
  own expiry is a convenience only.
- A new session token is issued on login (no session fixation). Logout deletes the row, so
  a stolen cookie dies with it.
- Expired rows are deleted by a periodic sweep.

## 3. CSRF

Session cookies are `SameSite=Lax`, which already blocks cross-site POSTs from forms, plus
an explicit double-submit token:

- On login the server sets a non-`HttpOnly` `csrf_token` cookie (32 random bytes).
- Every unsafe request (`POST`, `PUT`, `PATCH`, `DELETE`) must echo it in `X-CSRF-Token`.
- The middleware compares the two in constant time and rejects on mismatch.

The CSRF token is bound to the session row, so it rotates with the session.

## 4. Transport and headers

HTTPS is mandatory in production and terminated at the reverse proxy. The API sets:

```text
Strict-Transport-Security: max-age=31536000; includeSubDomains   (production only)
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Content-Security-Policy: default-src 'self'; frame-ancestors 'none'
```

CORS is an explicit allow-list from `CORS_ORIGINS` with credentials enabled — never `*`,
which is incompatible with cookie auth anyway.

## 5. Authorization

Four roles (plan §6):

| Role | Scope |
|---|---|
| `OWNER` | everything: settings, users, audit, backups |
| `OPERATOR` | customers, documents, payments, expenses, delivery |
| `ACCOUNTANT` | read, reports, exports, attachments |
| `READ_ONLY` | view only |

Enforcement is server-side middleware per route. The UI hides what a role cannot do, but
hiding is cosmetic — the API is the boundary. Deny is the default: a route without an
explicit role requirement is unreachable while authenticated as anything but `OWNER`.

## 6. Input handling

- Request bodies are size-limited and decoded with `DisallowUnknownFields`.
- All SQL is parameterized via `pgx`. String concatenation into SQL is prohibited.
- Uploads (Phase 5) are validated by sniffed content type, not by filename or client type,
  are size-capped, stored under a generated name outside the web root, and are served with
  `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff`.
- Rendering user text into document HTML templates escapes by default
  (`html/template`), and the renderer runs with JavaScript disabled, so a
  document's content cannot execute anything during rendering.
- The stored PDF path is resolved against the storage root and any path that
  would escape it is refused, so a crafted identifier cannot read another file.

## 7. Secrets

No secret is committed. Configuration comes from the environment; `.env` is git-ignored and
`.env.example` holds only placeholders. The database and object-storage credentials are
least-privilege: the application role owns its tables but is not a superuser and has no
`DROP DATABASE` right.

`BOOTSTRAP_OWNER_PASSWORD` exists only to create the first `OWNER`. It is consumed on first
start and must then be cleared from the environment.

## 8. Logging

Structured JSON with a request ID per request. Never logged: passwords, session tokens,
CSRF tokens, cookie values, `Authorization` headers, full customer identity numbers.
Errors are logged with their code and request ID; the client sees a Hebrew message and the
request ID, never a stack trace or SQL text.

## 9. Auditing

Append-only `audit_events` covering logins and security events, document issuance and
cancellation, payment and receipt actions, delivery attempts, settings and regulatory
changes, user and role changes, exports, and backup/restore (plan §16). Update and delete
are blocked at the database level, not only in application code.

## 10. Data protection

- Backups are **not encrypted** — a deliberate amendment to plan.md §18, recorded
  in `docs/SECURITY-REVIEW.md`. An encrypted backup whose key is lost is not a
  backup, and for a single operator that is the larger risk. The consequence:
  **the synced folder is the security boundary**, because the archive holds
  customer identity data and every financial record in the clear.
- Backups run daily into `BACKUP_DIR` for the operator's own sync tool to upload,
  and are restore-tested automatically (`TestRestoreFromBackupSmokeTest`) as well
  as manually per `docs/RUNBOOK.md`.
- Backup rotation is independent of legal retention: issued financial records are never
  purged because a backup aged out.
- Issued PDFs are stored with a SHA-256 hash, verified on every read: a file
  that no longer matches is refused rather than served.
- Issued documents cannot be altered or deleted at all — a database trigger
  refuses it, so an application bug or a stray `UPDATE` cannot rewrite a
  financial record.
- The PDF renderer is reachable only from the API inside the Compose network; it
  is never published to the host in production.

## 11. Deployment assumptions

These controls are sized for one private, biometrically locked machine, operated
by one person, with no internet exposure. HTTPS, CSRF, rate limiting, RBAC and
session hardening are all still in place — they protect against software faults
and mistakes, not only against remote attackers — but the MFA and backup
decisions above depend on this context. If it changes, re-read
`docs/SECURITY-REVIEW.md`, "Accepted risks".

## 12. Reporting a problem

This is an internal system. Report suspected vulnerabilities directly to the owner; do not
open a public issue.
