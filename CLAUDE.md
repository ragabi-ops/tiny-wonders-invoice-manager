# CLAUDE.md — Tiny Wonders Invoice & Receipt System

Project-level instructions. The global brain bootstrap in `~/.claude/CLAUDE.md`
(SOUL / USER / MEMORY / HEARTBEAT) still applies; this file adds what is
specific to this repository.

## 0. Session init — do this first, before anything else

**Read memory before reading code.** This project has settled decisions that the
source does not explain and that are expensive to re-derive: why numbering works
the way it does, why duplicates warn instead of blocking, which migration edits
will refuse to start, what the compliance gate blocks. Re-deriving them wastes a
session and risks re-litigating something already decided.

Run these at the start of every session, in order:

```bash
# 1. The project's distilled knowledge — decisions, gotchas, current phase.
~/ClaudeBrain/.venv/bin/python ~/ClaudeBrain/scripts/project_kb.py \
  show tiny-wonders-invoice-manager

# 2. Anything relevant to the task at hand, from the wider brain.
~/ClaudeBrain/.venv/bin/python ~/ClaudeBrain/scripts/memory_search.py \
  "<what this session is about>"
```

Then read `plan.md` for the phase you are working in, and the owning doc for the
area you are touching (table below).

Treat the KB as authoritative on **why**, and the code as authoritative on
**what**. If they disagree, the code is current and the KB is stale — say so and
fix the KB.

Before you finish a session of any substance, write back what was decided:

```bash
# A durable decision, fact, or open question.
~/ClaudeBrain/.venv/bin/python ~/ClaudeBrain/scripts/project_kb.py \
  add tiny-wonders-invoice-manager --section "Key Decisions" --item "..."

# A dated activity note.
~/ClaudeBrain/.venv/bin/python ~/ClaudeBrain/scripts/project_kb.py \
  note tiny-wonders-invoice-manager "..."

# Make it retrievable.
~/ClaudeBrain/.venv/bin/python ~/ClaudeBrain/scripts/memory_ingest.py --all
```

Use the CLI, not a text editor, so the KB and the journal stay separate. Record
only what is durable and not derivable from the repo — never file structure,
past fixes, or anything already written in `ARCHITECTURE.md`. Never write
secrets, credentials or customer data into the vault.

## What this is

An internal Hebrew/RTL invoice and receipt system for a small Israeli business
operating as **`עוסק פטור`** (exempt dealer). It handles real money and produces
documents that will eventually have legal weight.

**[`plan.md`](./plan.md) is the product and architecture baseline.** When this
file and `plan.md` disagree, `plan.md` wins. Owning docs:

| Question | Read |
|---|---|
| What are we building, and in what order? | `plan.md` |
| How is a rule actually implemented? | `ARCHITECTURE.md` |
| Why is this the security posture? | `SECURITY.md` |
| What does an endpoint accept and return? | `docs/openapi.yaml` |
| How do I run it? | `README.md` |
| How do I restore a backup, or diagnose a failure? | `docs/RUNBOOK.md` |
| What was reviewed, and which risks are accepted? | `docs/SECURITY-REVIEW.md` |

## Guarantees that must never be weakened

These come from `plan.md` §3 and §22.5. They are enforced in the **database**,
not only in Go. Do not remove a trigger or a constraint to make a change easier.

1. **No duplicate official numbers.** Allocation happens inside the issuance
   transaction; a partial unique index is the backstop.
2. **Issued documents are never edited or deleted.** The only permitted change
   is `ISSUED → CANCELLED`, setting only the cancellation fields.
3. **Payment and receipt commit together**, or not at all.
4. **Historical artifacts are preserved.** Snapshots, PDFs, and the logo file a
   document was issued with. Never overwrite one.
5. **The server computes every total, number and timestamp.** A request body
   carrying a total is rejected, not ignored.
6. **Money is exact.** `int64` agorot everywhere; quantities are integer
   thousandths. No float ever touches a monetary value.
7. **Regulation is configuration.** Effective-dated rows in
   `regulatory_config`, never Go constants.
8. **Delivery failure never rolls back an issued document.** Sending runs in a
   background job, after issuance has committed.
9. **A backup is only real once restored.** The restore path is tested, not
   assumed; `pg_dump` must match the server major version or archives restore
   badly while reporting success.

## Rules of engagement

- **Do not invent Israeli legal or accounting rules.** If the answer is not in
  `plan.md` or already configured, the correct behaviour is to refuse and
  surface the question — not to guess a plausible default. `internal/compliance`
  is where unresolved legal behaviour lives.
- **Never edit an applied migration.** The runner checksums them and will refuse
  to start. Add a new one; a financial database is repaired forward. (This has
  already bitten once, on a comment-only edit.)
- **Never edit a template version that has issued a document.** Add
  `templates/document/vN+1.html` and bump `CurrentDocumentVersion`. Duplication
  is the intended cost.
- **All user-facing text is Hebrew.** Code, identifiers, API fields, error
  `code`s and comments stay English. Error `message`s are Hebrew.
- **Add tests with every financial change** (`plan.md` §19). The concurrency
  tests in `tests/numbering_concurrency_test.go` are a hard gate.
- **No new infrastructure without a demonstrated need** (`plan.md` §22.9).
  Postgres, one Go binary, one SPA, Gotenberg. That is the whole stack.

## Layout

```text
apps/api        API entrypoint (thin; wiring lives in internal/app)
apps/web        Hebrew RTL React app
internal/       one package per domain slice; Service = rules, Handlers = HTTP
migrations/     ordered, forward-only SQL, embedded via go:embed
templates/      versioned document HTML; never edit a used version
tests/          integration tests against a real database
```

## Running it

```bash
make db-up      # PostgreSQL + Gotenberg
make migrate
make api        # :8090 locally (8080 is taken on this machine)
make web        # :5173
make check      # lint + unit; add `make test-db` for integration
```

Machine-specific ports, because 5432 and 8080 are already in use here:
`DEV_DB_PORT=5433`, `HTTP_ADDR=:8090`, `VITE_API_PORT=8090`, `DEV_PDF_PORT=3000`.

`timeout(1)` is not installed; use `go test -timeout Ns`.

### The live system is `make up`, not `make api`

```bash
make up         # Caddy + api + postgres + gotenberg
                # https://tiny-wonders-invoice-manager.com:9843
make down
make logs
```

The hostname is an `/etc/hosts` line pointing at `127.0.0.1`; Caddy is published
on `127.0.0.1:9843` only. Caddy serves the SPA from its own image and proxies
`/api` — there is no separate web server, so security headers and cache rules
live only in `deployments/Caddyfile`.

Two things follow, and both have already caused a real problem once:

- **`make up` and `make api` are different databases.** Real data is only in the
  stack's PostgreSQL, which publishes no host port. Never assume a row you see
  on `:5433` is in the live system.
- **They back up to different folders**, `STACK_BACKUP_DIR` and `BACKUP_DIR`.
  Do not point both at one folder: archive names carry only a timestamp, so the
  dumps become indistinguishable and retention prunes across both.

TLS is Caddy's own CA (`tls internal`) — a hosts-file name is not one a public
authority could issue for. `APP_ENV=production` and `SECURE_COOKIES=true` are
**hardcoded in `docker-compose.yml`, not read from `.env`**, because `.env`
describes the plain-HTTP development server. `config.go` refuses `production`
while cookies are insecure, so the two always move together — never flip one
alone.

The CA root lives in the `caddy-data` volume. `docker compose down -v` destroys
it, and the next start generates a new one, so the root trusted in Keychain
becomes stale and the browser will object. Re-trust the new root, or do not use
`-v` on this stack.

## Compliance gate — read before touching issuance

`real_issuance_enabled` is **false**. Every item in `plan.md` §2 is
`UNRESOLVED`. Until a tax adviser signs off:

- documents issue into the **`TEST` series**, never the official `A` series
- they are flagged `test_mode` and carry a Hebrew watermark
- nothing issued now can consume an official number

Do not flip that flag, and do not work around it.

## ClaudeBrain — where things live

The session-start and session-end commands are in §0. This is the map.

| Layer | Path | Holds |
|---|---|---|
| Project KB | `~/ClaudeBrain/vault/projects/tiny-wonders-invoice-manager/KB.md` | Distilled decisions, facts, open questions, timeline |
| Project journal | `.../tiny-wonders-invoice-manager/journal/YYYY/MM/` | Dated raw activity; not indexed for retrieval |
| Core memory | `~/ClaudeBrain/vault/00-core/MEMORY.md` | Cross-project durable index |

Slash commands wrap the same scripts: `/recall-memory` before re-deriving
something already settled, `/save-knowledge` to write a decision back.

Reflect at the end of a long session: what changed, what was decided, what
surprised us. The surprises are usually the most valuable entry.

## Deployment context

One private computer, biometric device access, one operator, no internet
exposure, not multi-tenant. Two `plan.md` requirements were amended on that
basis (both marked `AMENDED 2026-09-08` in the plan, with reasoning in
`docs/SECURITY-REVIEW.md`):

- **No MFA.** The device supplies the second factor.
- **Backups are not encrypted.** A lost key is the larger real risk; the synced
  folder is the security boundary instead.

Both must be revisited the moment the system becomes reachable from another
machine or a second person gets an account. Do not quietly extend these
relaxations to anything else.

## Current state

Phases 1–6 of `plan.md` §20 are complete: foundation, core data, documents,
payments and receipts, operations, production hardening. **Only Phase 7 remains,
and it needs a tax adviser rather than code.**

Not yet built, deliberately: VAT and tax invoices, credit documents, Israel
Invoice allocation numbers, OCR, WhatsApp Business API. See `plan.md` §21 for
the full non-goals list.
