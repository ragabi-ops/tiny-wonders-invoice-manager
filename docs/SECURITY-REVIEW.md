# Security review — 2026-09-08

Phase 6 review of the whole system before it holds real financial records
(`plan.md` §20). This records what was examined, what was found and fixed, and
which risks are accepted rather than solved.

**Scope.** All Go under `internal/` and `apps/api`, the SQL migrations, the
document templates, the deployment configuration, and the SPA's handling of
credentials and uploads.

**Deployment context.** A single instance on one private computer, protected by
biometric device access, operated by one person, with no internet exposure and
no multi-tenancy. The device is the outer boundary; the application controls are
what protects data from mistakes and from software faults, not from a remote
attacker who does not have a route in. Several judgements below rest on that —
each is marked with what would make it wrong.

---

## Found and fixed in this review

### 1. API responses carried no cache directive — fixed

Every API response contains customer identity data or financial records. None
of them set `Cache-Control`, so a browser could keep them on disk and serve
them from the back/forward cache after a logout.

**Fixed:** `Cache-Control: no-store` on every API response
(`internal/httpx/middleware.go`). The logo endpoint overrides it deliberately
with `private, max-age=300` — it is not sensitive and is fetched on every page.

### 2. The SPA was served with no security headers of its own — fixed

`deployments/Caddyfile` set a few headers, but `web.nginx.conf` — which serves
the bundle directly in some setups — set none. Anything relying on the proxy
being the one in this repository was relying on an assumption.

**Fixed:** a full header set at the origin, including a Content Security Policy
(`default-src 'self'`, `object-src 'none'`, `base-uri 'none'`,
`frame-ancestors 'none'`). The bundle is self-contained — no CDN, no external
fonts, no inline script — so the policy is strict without exceptions.

**Follow-up, 2026-09-08 — this fix was not in effect, and then the second server
was removed.** The headers were declared at `server` level in `web.nginx.conf`,
but nginx inherits `add_header` into a `location` block only while that block
declares none of its own. Both locations set a `Cache-Control`, so all four
security headers were discarded on every real request. The three that still
reached the browser did so because the Caddy in front happened to re-add exactly
those three — the proxy dependency this finding was written to remove. The CSP
was in no response at all. Verified by requesting the bundle from the origin
directly rather than through the proxy, which is the only way to see it.

The stack no longer has a second web server: Caddy serves the SPA from the image
built by `deployments/proxy.Dockerfile` and states the headers once, so there is
no inheritance rule to get wrong and no second origin to drift.

### 3. Stored paths could contain traversal segments — fixed

`storage.Store.Save` wrote to the correct, contained location, but returned the
*caller's* path, which was then persisted. A path holding `..` could be stored
in the database and replayed on every later read. It resolved safely each time,
so this was defence-in-depth rather than a live hole — but storing a
non-canonical path is a trap for whoever touches that code next.

**Fixed:** `Save` now returns the canonical store-relative path. Covered by
`TestPathsCannotEscapeTheStorageRoot`, which asserts both containment and that
the returned path has no traversal left in it.

### 4. A newer `pg_dump` produced archives that would not restore cleanly — fixed

This is the most serious finding, and it came from writing the restore test.

`pg_dump` 17 emits `SET transaction_timeout = 0`, which PostgreSQL 16 rejects.
The backup reported **success**; the restore was degraded. A backup that looks
fine and fails when you need it is worse than no backup, because you stop
worrying about it.

**Fixed** at three layers:
- `deployments/api.Dockerfile` pins `postgresql16-client` to match `postgres:16`.
- The backup service compares `pg_dump`'s major version with the server's, warns
  at startup, and reports the mismatch in `GET /api/v1/backups/status`.
- `TestRestoreFromBackupSmokeTest` restores into a scratch database and verifies
  the data **and** that the immutability triggers survived.

**Follow-up, 2026-09-08 — the second of those three layers was not working.**
`CheckToolVersions` read the server version with `SHOW server_version_num`, which
returns *text*, and scanned it into an `int`. The scan always failed, so the
function always returned an error and the comparison never ran. The failure took
the shape a safety net must never take: `GET /api/v1/backups/status` quietly
omitted its `tools` block, and startup logged `cannot run pg_dump; backups will
fail` on every boot regardless of pg_dump's actual health — an alarm that is
always on, which is the same as no alarm. A genuine version mismatch could not
have been reported. Fixed by scanning text and converting, and pinned by
`TestBackupStatusReportsToolVersions`, which asserts the comparison agrees with
itself in both directions. The lesson generalises: a guard that errors out looks
exactly like a guard that passes unless a test asserts it actually ran.

---

## Examined and found sound

**SQL injection.** Every query is parameterized through `pgx`. The only SQL text
built by concatenation uses package-level constants (column and FROM clauses).
The single user-influenced fragment is `date_trunc`'s unit in
`reports.RevenueByPeriod`, which comes from a closed `switch` whose default
returns an error — the request value never reaches the string.

**Credential handling.** Argon2id (64 MiB, t=3, p=2), parameters encoded in each
hash so they can be raised later. No password reaches any log call. Login
failures are indistinguishable between "no such account" and "wrong password",
and the unknown-account path burns comparable CPU so timing does not leak
either.

**Sessions.** The database stores only `sha256(token)`; a dump yields nothing
usable. New token on every login (no fixation). Absolute and idle timeouts are
enforced server-side, not by cookie expiry. Logout and deactivation destroy
sessions immediately.

**CSRF.** `SameSite=Lax` plus a double-submit token bound to the session row,
compared in constant time. Verified by test.

**Authorization.** Deny by default: every route sits behind `RequireAuth` and
carries an explicit role requirement. Tested per role for every write path,
including backups (OWNER-only) and exports (OWNER and ACCOUNTANT).

**Uploads.** Type decided by sniffing bytes, never the filename or declared
type. Size-capped before parsing. Stored under generated names. Served only as
`Content-Disposition: attachment` with `nosniff`. SVG is refused for logos — it
is script-capable and would be embedded into a page Chromium renders.

**Rendering.** `html/template` escapes by default. The renderer runs with
JavaScript disabled and no network access; templates are self-contained, which
a test enforces by rejecting any `http://` or `<script>` in rendered output.

**Error handling.** One envelope. The internal cause is logged and never
serialized — no stack traces, no SQL text, no driver messages reach a client.

**Integrity.** Every stored artifact carries a SHA-256 verified on read; a
tampered file is refused rather than served. Issued documents, their lines,
receipted payments and their allocations are immutable at the **database**
level, and the restore test confirms those triggers come back after a restore.

---

## Accepted risks

These are decisions, not oversights. Each is the owner's call, recorded with its
consequence.

### No multi-factor authentication

`plan.md` §6 recommended TOTP for OWNER; the plan was amended on 2026-09-08 to
drop it. The machine is biometrically locked and unreachable from the network,
so the device already supplies a second factor, and TOTP would add a
recoverable-secret problem without closing a reachable path.

*Consequence:* once someone is at an unlocked machine, the application password
is the only remaining barrier.

*Reinstate if:* the system becomes reachable from another machine, or a second
person gets an account.

### Backups are not encrypted

`plan.md` §18 asked for encrypted backups; the plan was amended on 2026-09-08.

*Rationale, and it is a real one:* an encrypted backup whose key is lost is not
a backup. For a single operator on a locked machine, key management is a bigger
practical risk than the archive being readable at rest.

*Consequence:* the archive holds customer names, ID and business numbers,
addresses, phone numbers and every financial record, readable by anything that
can read the folder — including the cloud provider it is synced to. **The sync
folder is the security boundary.** It needs a strong password and MFA on that
account.

*Revisit if:* the sync destination becomes a shared or less trusted account, or
the business takes on staff.

### The site was served over plain HTTP — RESOLVED 2026-09-08

Briefly an accepted risk, now closed rather than accepted, so it is recorded
here as history and not as a standing posture.

The stack is published at `https://tiny-wonders-invoice-manager.com:9843` on
`127.0.0.1` only, and Caddy terminates TLS with its own CA (`tls internal`) —
the hostname resolves through `/etc/hosts`, so no public authority could ever
issue for it. Port 80 is not published at all, which means there is no
plain-HTTP listener to downgrade onto.

`APP_ENV=production` and `SECURE_COOKIES=true` are therefore both in force, and
both are **hardcoded in `docker-compose.yml` rather than read from `.env`**. That
matters: `.env` configures a local `go run` against the throwaway development
database over plain HTTP, and letting its values reach this stack is precisely
how a development setting becomes the live one. The same file had already caused
one real incident this way, with backup directories.

*Verified, not assumed:* the chain validates against the extracted root without
`curl -k`; both cookies carry `HttpOnly` (session) and `Secure`; and
`Strict-Transport-Security` is present on the SPA navigation as well as on API
responses. That last one needed a fix — HSTS came only from the Go middleware, so
it was absent from `/`, which is the first thing a browser loads and the one
navigation the policy exists to protect. Caddy now states it for every response.

*What the operator owns:* Caddy's root CA must be trusted once
(`docs/RUNBOOK.md`). It lives in the `caddy-data` volume, so
`docker compose down -v` regenerates it and silently invalidates the trusted
root — another reason never to pass `-v` to this stack.

*Still true:* the certificate authenticates the server to this browser on this
machine. It is not a substitute for the loopback binding, which is what actually
keeps the system off the network.

### The bootstrap owner password travels through the environment

Necessary to create the first account. Mitigated: it is consumed only when the
database has no users, and the account is forced to change its password at first
login. Clear `BOOTSTRAP_OWNER_*` after the first start.

### `X-Forwarded-For` is ignored

Rate limiting and audit records use the peer address. Behind a proxy this is the
proxy. Deliberate: trusting a client-controlled header without a trusted-proxy
configuration would let anyone forge the source of an audit entry. Revisit
together with a real proxy configuration, not before.

---

## Standing recommendations

1. **Test a restore quarterly.** Follow `docs/RUNBOOK.md` into a scratch
   database. This review found a restore defect that a successful backup had
   hidden; only restoring finds those.
2. **Watch `/readyz`.** `backups: stale` means a daily run has been missed.
3. **Keep the client and server PostgreSQL major versions matched.**
4. **Re-run this review before enabling real issuance**, alongside the
   compliance gate — the two are the last checks before the system holds legally
   meaningful records.
