# Tiny Wonders — Invoice & Receipt System

Internal web application for a small Israeli business operating as **`עוסק פטור`** (exempt dealer).

- End-user UI: **Hebrew only, RTL**
- Currency: **ILS / ₪** — stored as integer **agorot**
- Timezone: **Asia/Jerusalem** (timestamps stored UTC)
- Code, API, docs: English

Product/architecture baseline: [`plan.md`](./plan.md).
Design: [`ARCHITECTURE.md`](./ARCHITECTURE.md). Security: [`SECURITY.md`](./SECURITY.md).
Operations and restore: [`docs/RUNBOOK.md`](./docs/RUNBOOK.md).
Review findings: [`docs/SECURITY-REVIEW.md`](./docs/SECURITY-REVIEW.md).

> **Not production-ready for real document issuance.** The compliance gate in `plan.md` §2
> must be cleared by a tax adviser first. Real issuance stays disabled until then.

## Stack

| Layer | Tech |
|---|---|
| API | Go 1.26, `net/http` + `chi`, `pgx/v5` |
| DB | PostgreSQL 16, plain SQL migrations |
| Web | React + TypeScript + Vite, TanStack Query, React Router, React Hook Form + Zod, Mantine (RTL) |
| PDF | Versioned HTML template → Gotenberg (headless Chromium) |
| Jobs | PostgreSQL-backed queue (`FOR UPDATE SKIP LOCKED`) — email, exports |
| Email | SMTP, optional; without it the UI offers WhatsApp share and download |
| Backups | Daily `tar.gz` (pg_dump + artifacts) into a folder you sync |
| Deploy | Docker Compose; Caddy serves the SPA and reverse-proxies `/api` |

## Requirements

Go 1.26+, Node 22+, Docker + Docker Compose.

## Quick start (local dev)

```bash
cp .env.example .env          # then edit BOOTSTRAP_OWNER_* to create the first user
make db-up                    # PostgreSQL and the PDF renderer, in Docker
make migrate                  # apply SQL migrations
make api                      # API on http://localhost:8090
make web                      # Web on http://localhost:5173
```

The PDF renderer is not optional: without it, issuing a document is refused
rather than producing a document with no artifact.

Email is optional. Leave `SMTP_HOST` empty and the UI offers WhatsApp share and
PDF download instead of a send that cannot work.

Backups run daily — a plain `tar.gz` of the database and every stored file, for
your own sync tool to upload. The development database writes to `BACKUP_DIR`
(default `./backups`); the full stack writes to `STACK_BACKUP_DIR`. Take one on
demand with `go run ./apps/api -backup-now`, or inside the stack with
`docker compose -f deployments/docker-compose.yml exec api /app/api -backup-now`.
Restore steps are in the runbook; **practise them before you need them** — a
backup is not real until it has been restored once.

`pg_dump` must match the server's major version or archives restore badly while
reporting success. The stack's image pins `postgresql16-client` to its
`postgres:16`; for local development set `PG_DUMP_PATH` if you have several
clients installed. `GET /api/v1/backups/status` reports both versions.

First run creates the `OWNER` user from `BOOTSTRAP_OWNER_EMAIL` / `BOOTSTRAP_OWNER_PASSWORD`,
flagged to change its password at first login. Clear those variables afterwards.

Ports are configurable because 5432 and 8080 are often already taken: `DEV_DB_PORT`
(default 5433), `DEV_PDF_PORT` (default 3000) and `HTTP_ADDR`. Set `VITE_API_PORT` to
match `HTTP_ADDR` so the Vite dev server proxies to the right place.

## Full stack in Docker — this is the one that holds real data

```bash
cp .env.example .env   # set POSTGRES_PASSWORD and STACK_BACKUP_DIR
make up                # Caddy + api + postgres + gotenberg
make down
```

Then open **https://tiny-wonders-invoice-manager.com:9843**. Two one-time steps:

```bash
# 1. Resolve the hostname locally.
echo '127.0.0.1  tiny-wonders-invoice-manager.com' | sudo tee -a /etc/hosts
sudo dscacheutil -flushcache

# 2. Trust Caddy's own certificate authority, so the certificate is genuinely
#    valid instead of a warning to click past.
docker compose -f deployments/docker-compose.yml --env-file .env exec proxy \
  cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy-root.crt
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain /tmp/caddy-root.crt
```

Caddy is the only web server: it serves the SPA bundle baked into its image and
reverse-proxies `/api`, so the app is same-origin and needs no CORS entry. It is
published on `127.0.0.1:${HTTPS_PORT}` (default 9843) — loopback only, so the
system is reachable from this machine and nowhere else. Port 80 is not
published at all, so there is no plain-HTTP listener to downgrade onto.

TLS is terminated by Caddy using its own CA (`tls internal`), because a hostname
that resolves through `/etc/hosts` is not one a public authority could ever
issue for. That is what lets the stack run as `APP_ENV=production` with
`SECURE_COOKIES=true`: `config.go` refuses `production` while cookies lack the
`Secure` flag, so the two always move together. `docker-compose.yml` hardcodes
both rather than reading `.env`, which describes the plain-HTTP development
server instead.

### The stack and `make api` are two different databases

`make up` runs its own PostgreSQL inside Docker; `make api` runs against the
throwaway development database on `DEV_DB_PORT`. They are not the same data, and
they back up to different folders on purpose — `STACK_BACKUP_DIR` for the real
system, `BACKUP_DIR` for development. Pointing both at one folder interleaves
their archives under identical names, and a restore then has no way to tell
which database a given dump came from.

## Moving to another Mac

```bash
./scripts/setup-new-mac.sh                       # fresh machine, empty system
./scripts/setup-new-mac.sh --restore <archive>   # fresh machine, bring the data
```

Installs Homebrew and Docker Desktop if missing, sets Docker to start at login,
writes a `.env` with a generated database password, creates the backup folder,
adds the hosts entry, builds and starts the stack, and trusts Caddy's CA. Every
step checks whether it is already done, so re-running it is safe.

With `--restore` it replaces the database and the stored files from a backup
archive, and asks before doing so. It restores into PostgreSQL *before* starting
the API, so the API cannot migrate and bootstrap an owner into the database that
is about to be overwritten.

Go and Node are not installed: the stack builds inside Docker, so a machine that
only runs the system needs neither. Install them to develop on it.

## Turning on real issuance

```bash
./scripts/enable-real-issuance.sh
```

Interactive and audited: it prints the seven `plan.md` §2 items you are
declaring answered, asks who cleared them, then signs in as OWNER and writes a
new effective-dated `compliance_gate` row through the API — so `audit_events`
records which account did it. Your password is read from the terminal into the
request and never becomes a command-line argument or a file.

**This is not reversible in the way people expect.** Closing the gate again
later does not un-issue documents or return official numbers that have already
been consumed.

## Make targets

| Target | Does |
|---|---|
| `make db-up` / `make db-down` | local PostgreSQL and Gotenberg |
| `make migrate` | apply pending migrations |
| `make migrate-status` | list applied/pending migrations |
| `make api` | run API with live `.env` |
| `make web` | run Vite dev server |
| `make test` | Go tests (`-race`); database tests skip without `TEST_DATABASE_URL` |
| `make test-db` | Create `invoice_test` and run the database integration tests |
| `make test-web` | web typecheck + build |
| `make lint` | `go vet` + `tsc --noEmit` |
| `make up` / `make down` | full Docker Compose stack (Caddy + api + postgres + gotenberg) |

## Tests

```bash
make test       # unit tests; database tests skip without TEST_DATABASE_URL
make test-db    # integration tests against a throwaway invoice_test database
```

The integration tests drop and recreate the `public` schema, so point
`TEST_DATABASE_URL` only at a throwaway database. `make test-db` also points
`TEST_GOTENBERG_URL` at the local renderer; tests that issue documents skip
without it.

The **numbering concurrency tests** in `tests/numbering_concurrency_test.go` are the
gate `plan.md` §20 puts in front of everything after Phase 3. They must pass.

## Layout

```text
apps/api      API entrypoint
apps/web      Hebrew RTL React app
internal/     domain packages (auth, customers, documents, payments, ...)
migrations/   ordered SQL migrations
templates/    versioned document HTML templates, embedded in the binary
deployments/  Docker Compose + Dockerfiles
docs/         OpenAPI spec and notes
tests/        cross-package / integration tests
```

## Implementation status

Phases follow `plan.md` §20.

- [x] **Phase 1 — Foundation**: repo, Compose, migrations, Go API, RTL React shell, auth/RBAC, settings
- [x] **Phase 2 — Core data**: customers with duplicate warnings, service catalogue, activities, global search
- [x] **Phase 3 — Documents**: drafts, server-computed totals, atomic issuance with the sequence engine, immutable snapshots, Hebrew RTL PDFs
- [x] **Phase 4 — Payments and receipts**: allocation, partial and split payments, receipts issued in the same transaction, idempotency, customer balance and timeline
- [x] **Phase 5 — Operations**: email and WhatsApp delivery on a background queue, expenses with attachments, real dashboard and reports, accountant export ZIP
- [x] **Preview before issuing** — check a draft, or the receipt a payment would produce, without allocating or storing anything
- [x] **Phase 6 — Production hardening**: daily backups with a tested restore, observability, security review, template regression tests. MFA and backup encryption deliberately dropped for this deployment — see `plan.md` amendments and `docs/SECURITY-REVIEW.md`
- [ ] **Phase 7 — Compliance gate and go-live** — the only phase left. Needs a tax adviser, not code.
