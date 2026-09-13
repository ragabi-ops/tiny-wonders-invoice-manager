#!/usr/bin/env bash
#
# Open the compliance gate, so documents issue into the official "A" series
# instead of the watermarked TEST series.
#
#   ./scripts/enable-real-issuance.sh
#
# This is the single most consequential setting in the system, so it is a
# deliberate, interactive, audited act and not a config flag:
#
#   - it goes through POST /api/v1/settings/regulatory, which writes an
#     audit_events row naming the account that did it
#   - your password is read straight into this script and used to log in; it is
#     never passed as an argument, never written to a file, and never appears in
#     shell history
#   - regulatory_config is append-only, so this adds a new effective-dated row
#     and the previous one stays readable forever
#
# What it does NOT do is answer the seven questions in plan.md §2. It records
# that YOU are declaring them answered. If a tax adviser has actually signed
# off, say so at the prompt and their name goes into the permanent record.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

HOSTNAME_LOCAL="tiny-wonders-invoice-manager.com"
HTTPS_PORT_VALUE="$(grep -E '^HTTPS_PORT=' .env 2>/dev/null | tail -1 | cut -d= -f2- || true)"
BASE_URL="https://$HOSTNAME_LOCAL:${HTTPS_PORT_VALUE:-9843}"

if [[ -t 1 ]]; then
	B=$'\033[1m'; DIM=$'\033[2m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; R=$'\033[0m'
else
	B=""; DIM=""; GREEN=""; YELLOW=""; RED=""; R=""
fi
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$R" "$1"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$R" "$1"; }
die()  { printf '\n  %sx%s %s\n\n' "$RED" "$R" "$1" >&2; exit 1; }

# The seven items from plan.md §2, in the order the plan lists them.
ITEMS=(
	"required_fields_and_wording:required fields and wording per document"
	"receipt_timing_and_payment_details:receipt timing and payment-detail requirements"
	"cancellation_and_correction:cancellation and correction behaviour"
	"computerized_document_and_signature:computerized-document and signature requirements"
	"legal_retention:legal retention requirements"
	"software_registration_required:whether this implementation requires software registration"
	"accountant_export_requirements:accountant and export requirements"
)

cat <<EOF

${B}Opening the compliance gate${R}

Once this is done, the next document you issue takes an official number in
series ${B}A${R}, with no watermark, and it is ${B}immutable${R}. An official number that
has been consumed cannot be returned, and closing the gate again later does
not undo documents already issued under it.

You are declaring that these have been answered for this business:

EOF

for entry in "${ITEMS[@]}"; do
	printf '  %s.%s %s\n' "$DIM" "$R" "${entry#*:}"
done

cat <<EOF

${DIM}plan.md §2 also notes that the Tax Authority generally requires registration
for accounting software intended for third-party use, and describes an
internal-development exception separately. Because this was built with AI
assistance, the plan's standing instruction is not to assume the exception
applies.${R}

EOF

read -r -p "Type 'I accept responsibility' to continue: " consent
[[ "$consent" == "I accept responsibility" ]] || die "cancelled — nothing was changed"

printf '\n'
read -r -p "Who cleared these items? (a tax adviser's name, or your own): " cleared_by
[[ -n "$cleared_by" ]] || die "the record needs a name"

read -r -p "Note for the permanent audit record (optional): " note
if [[ -z "$note" ]]; then
	note="Compliance gate opened by $cleared_by. Recorded via scripts/enable-real-issuance.sh."
fi

printf '\n'
read -r -p "OWNER email: " owner_email
[[ -n "$owner_email" ]] || die "an email is needed to log in"
read -r -s -p "OWNER password (hidden): " owner_password
printf '\n\n'
[[ -n "$owner_password" ]] || die "a password is needed to log in"

# ------------------------------------------------------------------- do it --

jar="$(mktemp -t invoice-session)"
cleanup() { rm -f "$jar"; unset owner_password; }
trap cleanup EXIT

login_status="$(
	printf '%s' "$owner_password" | python3 -c '
import json, sys
print(json.dumps({"email": sys.argv[1], "password": sys.stdin.read()}))
' "$owner_email" \
	| curl -s -o /dev/null -w '%{http_code}' -c "$jar" \
		-H 'Content-Type: application/json' -X POST "$BASE_URL/api/v1/auth/login" --data-binary @-
)"

[[ "$login_status" == "200" ]] || die "login failed (HTTP $login_status). Check the email and password, and that the stack is up."
ok "signed in as $owner_email"

csrf="$(awk '$6=="csrf_token"{print $7}' "$jar" | tail -1)"
[[ -n "$csrf" ]] || die "no CSRF token in the session — is this the right server?"

items_json="$(
	for entry in "${ITEMS[@]}"; do printf '%s\n' "${entry%%:*}"; done \
	| python3 -c 'import json,sys; print(json.dumps({k.strip(): "RESOLVED" for k in sys.stdin if k.strip()}))'
)"

payload="$(python3 - "$items_json" "$cleared_by" "$note" <<'PY'
import json, sys
from datetime import datetime, timezone

items, cleared_by, note = json.loads(sys.argv[1]), sys.argv[2], sys.argv[3]
print(json.dumps({
    "key": "compliance_gate",
    # Today, in the business timezone as the server reads it. Regulatory rows
    # are effective-dated; this one takes effect from today forward and the
    # previous row stays queryable for every document issued before it.
    "effective_from": datetime.now().astimezone().strftime("%Y-%m-%d"),
    "note": note,
    "value": {
        "real_issuance_enabled": True,
        "cleared_by": cleared_by,
        "cleared_at": datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z"),
        "items": items,
    },
}))
PY
)"

status="$(
	printf '%s' "$payload" | curl -s -o /tmp/gate-response.json -w '%{http_code}' -b "$jar" \
		-H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" \
		-X POST "$BASE_URL/api/v1/settings/regulatory" --data-binary @-
)"

if [[ "$status" != "200" && "$status" != "201" && "$status" != "204" ]]; then
	printf '\n'
	sed 's/^/  /' /tmp/gate-response.json 2>/dev/null || true
	rm -f /tmp/gate-response.json
	die "the server refused the change (HTTP $status). Nothing was applied."
fi
rm -f /tmp/gate-response.json
ok "regulatory row written and audited"

# ----------------------------------------------------------------- confirm --

printf '\n'
compliance="$(curl -s -b "$jar" "$BASE_URL/api/v1/compliance")"
printf '%s' "$compliance" | python3 -m json.tool | sed 's/^/  /'

if printf '%s' "$compliance" | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if d.get("real_issuance_enabled") and not d.get("unresolved_gate_items") else 1)'; then
	printf '\n'
	ok "${B}Real issuance is live.${R} The next document takes official number A-1."
	warn "reload the browser: the מצב בדיקה badge is cached until the page refetches"
	printf '\n  %sTake a backup now, so you have a clean line between test and real:%s\n' "$DIM" "$R"
	printf '    docker compose -f deployments/docker-compose.yml --env-file .env exec api /app/api -backup-now\n\n'
else
	printf '\n'
	die "the gate still reports blocked — read the status above; nothing will issue into series A yet"
fi
