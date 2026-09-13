# Runbook

Operating procedures for the Invoice & Receipt System. Written to be followed
under pressure, by someone who did not write the software.

---

## Where it runs

The live system is the Docker Compose stack in `deployments/`, reached at
**https://tiny-wonders-invoice-manager.com:9843**. That hostname is a line in
`/etc/hosts` pointing at `127.0.0.1`; it is not DNS and resolves on this machine
only. Caddy is published on `127.0.0.1:9843`, so nothing on the network can
reach the application even when the laptop joins an untrusted one. Port 80 is
not published, so there is no plain-HTTP listener to downgrade onto.

```bash
make up                 # start (or rebuild and restart) the whole stack
make down               # stop it
make logs               # follow all container logs
docker compose -f deployments/docker-compose.yml --env-file .env ps
```

Containers are `restart: unless-stopped` and Docker Desktop is set to start at
login, so a reboot brings the system back on its own — which is what makes the
02:00 backup dependable. If the site does not answer after a restart, check that
Docker Desktop is running before anything else.

**`make api` is not this system.** It runs against the throwaway development
database on `DEV_DB_PORT` and serves no SPA. Real customers, documents and
payments live only in the stack's own PostgreSQL. The two also back up to
different folders on purpose — see below.

### The site does not load

```bash
docker compose -f deployments/docker-compose.yml --env-file .env ps   # all running?
curl -s https://tiny-wonders-invoice-manager.com:9843/readyz           # ready:true?
grep tiny-wonders /etc/hosts                                          # entry present?
```

A `readyz` that answers with `ready:false` names the failing check. A connection
refused with all containers running usually means the hosts entry is missing or
the DNS cache is stale: `sudo dscacheutil -flushcache`.

### The browser warns about the certificate

Caddy signs with its own CA, so that CA has to be trusted once:

```bash
docker compose -f deployments/docker-compose.yml --env-file .env exec proxy \
  cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy-root.crt
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain /tmp/caddy-root.crt
```

If it warned after working fine before, the CA was probably regenerated. The
root lives in the `caddy-data` volume, and `docker compose down -v` destroys it —
the next start creates a new CA, and the root you trusted no longer signs
anything. Trust the new root with the same command. **Do not use `-v` on this
stack**; it would take the database with it too.

---

## Moving to another machine

```bash
# on the old machine — take a fresh archive first
docker compose -f deployments/docker-compose.yml --env-file .env exec api /app/api -backup-now

# copy the repository and that archive across, then on the new machine
./scripts/setup-new-mac.sh --restore ~/backups/invoice-manager/backup-....tar.gz
```

The script is idempotent and asks before it replaces anything. It restores the
database *before* the API starts, because an API that starts first would migrate
an empty database and bootstrap a second owner into it.

Carry across, and check afterwards:

- **`.env`** — or let the script generate a new one. `POSTGRES_PASSWORD` may
  differ between machines; it protects a database that publishes no host port.
- **the backup folder**, and re-point your cloud sync at it.
- **the certificate.** Caddy generates a *new* CA on the new machine, so its
  root has to be trusted there too — the script does that.

Then verify against the real thing, not against the script's own output: log in,
open a document issued on the old machine, and download its PDF. That exercises
the database, the artifact store and the SHA-256 check in one action. A document
whose file did not come across refuses to download rather than serving something
wrong, so a successful download is real evidence.

---

## Backups

### What is backed up

One gzipped tar per day, named `backup-<UTC timestamp>.tar.gz`. The live stack
writes to `STACK_BACKUP_DIR` — **`~/backups/invoice-manager`, the folder to
sync**; a local `go run` writes to `BACKUP_DIR` (`./backups`), which is
development data and should never be restored over the real database.

Keeping them apart matters more than it looks. The archive name carries a
timestamp and nothing about which database produced it, so two databases writing
into one folder leaves you holding dumps you cannot tell apart — and retention
counts them together, so development archives can prune real ones. If you find
an archive in the real folder with no matching row in `backup_runs`, that is
where it came from; move it out.

```text
database.dump                  pg_dump custom format — everything in PostgreSQL
storage/documents/…            the issued PDFs
storage/attachments/…          expense receipts and supplier invoices
storage/business/logo/…        every logo ever uploaded
manifest.json                  what this archive is and how to restore it
```

Exports are **not** included: they are rebuilt on demand from the data above.

### What is not protected

The archive is **not encrypted**. It contains customer names, ID and business
numbers, addresses, phone numbers, and every financial record the business has.
Anything that can read the folder can read all of it.

That was a deliberate choice — see `SECURITY.md`, "Backups". The consequence you
own: **the folder you sync to is the security boundary.** Keep it in an account
with a strong password and multi-factor authentication, and do not share it.

### Schedule

`BACKUP_AT` (default `02:00`, business timezone), once a day. The rule is "if no
backup has succeeded since today's scheduled time, run one" — so a machine that
was asleep at 02:00 backs up when it wakes, rather than skipping the day.

`BACKUP_RETENTION_DAYS` (default 30) bounds how many archives stay in the
folder. **This is disk hygiene, not records retention.** Financial records live
in the database and the artifact store and are never pruned. The most recent
archive is never deleted, however old it is.

### Check that backups are working

Any of these:

```bash
curl -s https://tiny-wonders-invoice-manager.com:9843/readyz | grep backups   # "ok", "stale", or "not configured"
```

Or in the app: **הגדרות → גיבויים**. Or `GET /api/v1/backups/status` as OWNER.

`stale` means the newest successful backup is more than 36 hours old — a daily
schedule has missed at least one run. Investigate; do not ignore it.

### Take a backup right now

Before anything risky — an upgrade, a data fix, a migration:

```bash
go run ./apps/api -backup-now       # development
docker compose -f deployments/docker-compose.yml exec api /app/api -backup-now
```

No login needed. It prints where the archive landed.

---

## Restore

> Practise this on a scratch database **before** you need it. An untested backup
> is a hope. Schedule it: once a quarter, restore last night's archive into a
> throwaway database and confirm the numbers.

### 1. Stop the application

```bash
docker compose -f deployments/docker-compose.yml stop api
```

Nothing should be writing while you restore.

### 2. Unpack the archive

```bash
mkdir -p /tmp/restore && cd /tmp/restore
tar xzf /path/to/backup-2026-09-08T020000Z.tar.gz
cat manifest.json          # confirm you have the archive you think you have
```

### 3. Restore the database

Into an **empty** database. Never restore over a live one.

```bash
createdb invoice_restored
pg_restore --no-owner --no-privileges --dbname="postgres://…/invoice_restored" database.dump
```

`pg_restore` may report "errors ignored on restore". Read them. Settings the
server does not recognise are harmless; anything mentioning a table or a
constraint is not.

Check the data came back:

```sql
SELECT count(*) FROM documents WHERE state = 'ISSUED';
SELECT count(*) FROM payments;
SELECT max(number) FROM documents WHERE series = 'A';
```

And check the **guarantees** came back, not just the rows — this must fail:

```sql
UPDATE documents SET total_agorot = 1 WHERE state = 'ISSUED';
-- ERROR: issued document … cannot be modified
```

If that succeeds, the triggers did not restore and the database is not safe to
use. Stop and investigate.

### 4. Restore the files

```bash
rsync -a storage/ "$STORAGE_DIR"/
```

The PDFs must come back too. The database records a SHA-256 for every artifact
and verifies it on read, so a document whose file is missing will refuse to
download rather than serve something wrong.

### 5. Point the application at the restored database and start it

```bash
# DATABASE_URL=…/invoice_restored
docker compose -f deployments/docker-compose.yml start api
curl -s https://tiny-wonders-invoice-manager.com:9843/readyz
```

### 6. Verify before trusting it

- **הגדרות → גיבויים** — is the picture sensible?
- **מסמכים** — open the most recent issued document and download its PDF. If the
  hash check passes, the database and the files agree.
- **דוחות** — do the totals match what you expect for the period?

---

## PostgreSQL client version

`pg_dump` **must match the server major version**. A newer client writes
archives an older server cannot restore cleanly — pg_dump 17 emits
`SET transaction_timeout`, which PostgreSQL 16 rejects.

The API logs a warning at startup on a mismatch, and reports it in
`GET /api/v1/backups/status` under `tools`. The Docker image pins
`postgresql16-client` to match `postgres:16`. On a development machine with
several clients installed, point `PG_DUMP_PATH` at the matching one.

---

## Health and monitoring

| Endpoint | Answers |
|---|---|
| `GET /healthz` | Is the process alive? Touches nothing external. |
| `GET /readyz` | Can it serve? Database, migrations, PDF renderer, job queue, backups. |

`/readyz` returns 503 only when the API genuinely cannot serve — the database is
down, or migrations are pending. A stale backup, a failed job or a missing PDF
renderer are **reported but not fatal**: refusing all traffic because last
night's backup failed helps nobody.

Every response carries `X-Request-Id`. When a user reports an error, ask for the
request ID shown in the message and grep the logs for it.

---

## Common situations

### "A document will not issue"

Check `/readyz` for `pdf_renderer`. Without a renderer, issuance is **refused**
by design — an issued document must always have its PDF. No number is consumed
by the failed attempt; fix the renderer and issue again.

### "An email never arrived"

Open the document → **היסטוריית שליחה**. A `FAILED` attempt shows the reason.
The document itself is unaffected: delivery runs after issuance and can never
roll it back. Send again, or share by WhatsApp.

Deployment-wide: **ראשי** shows failed deliveries and failed jobs.

### "The numbers look wrong in a report"

Revenue counts issued, uncancelled invoices and payment requests. Receipts are
excluded deliberately — a receipt acknowledges money the settled document
already counts, so including both would double every figure.

### "Someone needs to be locked out now"

**משתמשים** → toggle the account off. Every session that user holds is destroyed
immediately, not at expiry.

### "The last OWNER left"

The system refuses to remove or disable the last active OWNER — by design, so it
cannot lock itself out of its own administration. Create the replacement OWNER
first, then disable the old one.

---

## Before real issuance — the compliance gate

Every document today is stamped **מסמך לבדיקה בלבד** and numbered in the `TEST`
series. That is not a bug. `plan.md` §2 lists seven questions a tax adviser must
answer first:

1. required fields and wording per document
2. receipt timing and payment-detail requirements
3. cancellation and correction behaviour
4. computerized-document and signature requirements
5. legal retention requirements
6. whether this implementation requires software registration
7. accountant export requirements

When they are answered:

```bash
./scripts/enable-real-issuance.sh
```

It prints the seven items, asks who cleared them, and writes a new
effective-dated `compliance_gate` row through the API as the signed-in OWNER, so
`audit_events` records the account that did it. Setting the flag alone is not
enough — `CanIssueForReal` also requires every item to read `RESOLVED`, so a
half-filled gate still blocks. There is no form for this in the UI on purpose.

Documents then issue into series `A` with no watermark. Nothing issued before
that point ever consumed an official number, and `TEST`-series documents keep
their own counter, so the two can never collide.

**What cannot be undone.** Closing the gate again later does not un-issue a
document or return an official number. Issued rows are immutable at the database
level; the only permitted change is `ISSUED → CANCELLED`, which preserves the
number and the artifact. Take a backup immediately before opening the gate, so
there is a clean line between the test period and the real one.
