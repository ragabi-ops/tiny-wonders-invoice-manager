#!/usr/bin/env bash
#
# Bring the Invoice & Receipt System up on a fresh Mac.
#
#   ./scripts/setup-new-mac.sh
#   ./scripts/setup-new-mac.sh --restore ~/backups/invoice-manager/backup-....tar.gz
#
# Safe to re-run: every step checks whether it is already done. Nothing here
# destroys data except --restore, which says so and asks first.
#
# What it deliberately does NOT install: Go and Node. The whole stack builds
# inside Docker, so a machine that only runs the system needs neither. Install
# them only to develop on it.
#
# What it deliberately does NOT do: open the compliance gate. Real issuance is
# a separate, audited decision — see scripts/enable-real-issuance.sh.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

COMPOSE=(docker compose -f deployments/docker-compose.yml --env-file .env)
HOSTNAME_LOCAL="tiny-wonders-invoice-manager.com"
COMPOSE_PROJECT="tiny-wonders-invoice"
RESTORE_ARCHIVE=""

# ------------------------------------------------------------------ output --

if [[ -t 1 ]]; then
	B=$'\033[1m'; DIM=$'\033[2m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; R=$'\033[0m'
else
	B=""; DIM=""; GREEN=""; YELLOW=""; RED=""; R=""
fi

step() { printf '\n%s==>%s %s%s%s\n' "$B" "$R" "$B" "$1" "$R"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$R" "$1"; }
skip() { printf '  %s·%s %s %s(already done)%s\n' "$DIM" "$R" "$1" "$DIM" "$R"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$R" "$1"; }
die()  { printf '\n  %sx%s %s\n\n' "$RED" "$R" "$1" >&2; exit 1; }

# ------------------------------------------------------------------- args ---

while [[ $# -gt 0 ]]; do
	case "$1" in
		--restore)
			[[ $# -ge 2 ]] || die "--restore needs the path to a backup archive"
			RESTORE_ARCHIVE="$2"; shift 2 ;;
		-h|--help)
			# Print the header block and stop at the first non-comment line, so
			# this stays right if the header grows.
			awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "${BASH_SOURCE[0]}"
			exit 0 ;;
		*)
			die "unknown argument: $1" ;;
	esac
done

if [[ -n "$RESTORE_ARCHIVE" ]]; then
	[[ -f "$RESTORE_ARCHIVE" ]] || die "no such archive: $RESTORE_ARCHIVE"
	RESTORE_ARCHIVE="$(cd "$(dirname "$RESTORE_ARCHIVE")" && pwd)/$(basename "$RESTORE_ARCHIVE")"
fi

# --------------------------------------------------------------- preflight --

step "Checking the machine"

[[ "$(uname -s)" == "Darwin" ]] || die "this script is for macOS; on Linux install Docker and run 'make up'"
[[ "$(id -u)" != "0" ]] || die "do not run this with sudo; it asks for a password only where root is genuinely needed"
ok "macOS $(sw_vers -productVersion) on $(uname -m)"

# Ask for sudo once, up front, rather than surprising the operator half way in.
if ! sudo -n true 2>/dev/null; then
	printf '  This script needs your password twice: to add a hosts entry and to\n'
	printf '  trust a local certificate authority. Nothing else uses root.\n\n'
	sudo -v || die "sudo is required"
fi

# ---------------------------------------------------------------- homebrew --

step "Homebrew"

if command -v brew >/dev/null 2>&1; then
	skip "Homebrew"
else
	/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
	# A fresh install is not on PATH yet for this shell.
	for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
		[[ -x "$candidate" ]] && eval "$("$candidate" shellenv)" && break
	done
	command -v brew >/dev/null 2>&1 || die "Homebrew installed but is not on PATH; open a new terminal and re-run"
	ok "Homebrew installed"
fi

# ------------------------------------------------------------------ docker --

step "Docker Desktop"

if [[ -d /Applications/Docker.app ]]; then
	skip "Docker Desktop"
else
	brew install --cask docker
	ok "Docker Desktop installed"
fi

if docker info >/dev/null 2>&1; then
	skip "Docker daemon running"
else
	open -a Docker
	printf '  waiting for the Docker daemon '
	for _ in $(seq 1 90); do
		if docker info >/dev/null 2>&1; then break; fi
		printf '.'; sleep 2
	done
	printf '\n'
	docker info >/dev/null 2>&1 || die "Docker did not start. Open Docker Desktop, accept its terms, then re-run."
	ok "Docker daemon running"
fi

# Containers are restart:unless-stopped, which only helps if Docker itself
# comes back after a reboot. Without this the daily 02:00 backup silently
# stops happening the first time the machine restarts.
DOCKER_SETTINGS="$HOME/Library/Group Containers/group.com.docker/settings-store.json"
if [[ -f "$DOCKER_SETTINGS" ]]; then
	if python3 -c "import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get('AutoStart') else 1)" "$DOCKER_SETTINGS" 2>/dev/null; then
		skip "Docker starts at login"
	else
		cp "$DOCKER_SETTINGS" "$DOCKER_SETTINGS.bak.$(date +%Y%m%d%H%M%S)"
		python3 - "$DOCKER_SETTINGS" <<-'PY'
			import json, sys
			path = sys.argv[1]
			data = json.load(open(path))
			data['AutoStart'] = True
			json.dump(data, open(path, 'w'), indent=2)
		PY
		ok "Docker set to start at login"
		warn "Docker Desktop can rewrite this when it quits — confirm in Settings → General"
	fi
else
	warn "Docker settings file not found; enable 'Start Docker Desktop when you sign in' by hand"
fi

# --------------------------------------------------------------------- env --

step "Configuration (.env)"

env_get() { grep -E "^$1=" .env 2>/dev/null | tail -1 | cut -d= -f2- || true; }

env_set() {
	local key="$1" value="$2"
	if grep -qE "^${key}=" .env; then
		python3 - "$key" "$value" <<-'PY'
			import re, sys
			key, value = sys.argv[1], sys.argv[2]
			with open('.env') as fh:
			    text = fh.read()
			text = re.sub(rf'(?m)^{re.escape(key)}=.*$', f'{key}={value}', text)
			with open('.env', 'w') as fh:
			    fh.write(text)
		PY
	else
		printf '%s=%s\n' "$key" "$value" >> .env
	fi
}

if [[ -f .env ]]; then
	skip ".env exists (leaving it alone)"
else
	cp .env.example .env
	ok ".env created from .env.example"

	env_set POSTGRES_PASSWORD "$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 40)"
	ok "POSTGRES_PASSWORD generated"

	env_set STACK_BACKUP_DIR "$HOME/backups/invoice-manager"
	env_set SITE_ADDRESS "https://$HOSTNAME_LOCAL"
	env_set HTTPS_PORT 9843
	env_set TIMEZONE Asia/Jerusalem
	env_set BUSINESS_MODE EXEMPT_DEALER
	ok "hostname, port, backup folder and timezone set"

	if [[ -n "$RESTORE_ARCHIVE" ]]; then
		# The restored dump already contains the users table. Leaving these
		# empty stops a second owner being created alongside the real one.
		env_set BOOTSTRAP_OWNER_EMAIL ""
		env_set BOOTSTRAP_OWNER_PASSWORD ""
		env_set BOOTSTRAP_OWNER_NAME ""
		ok "bootstrap owner left empty (the restore brings the real accounts)"
	else
		printf '\n  The first OWNER account is created from these.\n'
		read -r -p "  Email: " owner_email
		read -r -p "  Display name: " owner_name
		read -r -s -p "  Password (hidden; you must change it at first login): " owner_password
		printf '\n'
		[[ -n "$owner_email" && -n "$owner_password" ]] || die "an email and a password are needed to create the first account"
		env_set BOOTSTRAP_OWNER_EMAIL "$owner_email"
		env_set BOOTSTRAP_OWNER_PASSWORD "$owner_password"
		env_set BOOTSTRAP_OWNER_NAME "$owner_name"
		ok "bootstrap owner recorded in .env"
	fi
fi

BACKUP_DIR_HOST="$(env_get STACK_BACKUP_DIR)"
[[ -n "$BACKUP_DIR_HOST" ]] || die "STACK_BACKUP_DIR is not set in .env"
HTTPS_PORT_VALUE="$(env_get HTTPS_PORT)"; HTTPS_PORT_VALUE="${HTTPS_PORT_VALUE:-9843}"
BASE_URL="https://$HOSTNAME_LOCAL:$HTTPS_PORT_VALUE"

if [[ -d "$BACKUP_DIR_HOST" ]]; then
	skip "backup folder $BACKUP_DIR_HOST"
else
	mkdir -p "$BACKUP_DIR_HOST"
	chmod 700 "$BACKUP_DIR_HOST"
	ok "backup folder created: $BACKUP_DIR_HOST"
fi
warn "point your cloud sync at $BACKUP_DIR_HOST — the archives are NOT encrypted, so that folder is the security boundary"

# ------------------------------------------------------------------- hosts --

step "Hostname"

if grep -qE "^[^#]*[[:space:]]$HOSTNAME_LOCAL([[:space:]]|\$)" /etc/hosts; then
	skip "$HOSTNAME_LOCAL in /etc/hosts"
else
	sudo cp /etc/hosts "/etc/hosts.bak.$(date +%Y%m%d%H%M%S)"
	printf '\n# Tiny Wonders invoice & receipt system (local only)\n127.0.0.1\t%s\n' "$HOSTNAME_LOCAL" \
		| sudo tee -a /etc/hosts >/dev/null
	sudo dscacheutil -flushcache || true
	ok "$HOSTNAME_LOCAL now resolves to 127.0.0.1"
fi

# ------------------------------------------------------------------ restore --

if [[ -n "$RESTORE_ARCHIVE" ]]; then
	step "Restoring from $(basename "$RESTORE_ARCHIVE")"

	printf '  This REPLACES the database and stored files in this stack.\n'
	read -r -p "  Type 'restore' to continue: " confirm
	[[ "$confirm" == "restore" ]] || die "cancelled"

	work="$(mktemp -d)"
	trap 'rm -rf "$work"' EXIT
	tar xzf "$RESTORE_ARCHIVE" -C "$work"
	[[ -f "$work/database.dump" ]] || die "archive has no database.dump — is this a backup from this system?"
	if [[ -f "$work/manifest.json" ]]; then
		printf '  archive manifest:\n'
		sed 's/^/    /' "$work/manifest.json"
	fi

	# Postgres only. The API must not start yet: it would migrate and bootstrap
	# an owner into the database we are about to replace.
	"${COMPOSE[@]}" up -d --wait postgres
	pgpass="$(env_get POSTGRES_PASSWORD)"

	"${COMPOSE[@]}" exec -T postgres psql -U invoice -d postgres \
		-c "DROP DATABASE IF EXISTS invoice WITH (FORCE);" \
		-c "CREATE DATABASE invoice OWNER invoice;" >/dev/null
	ok "empty database created"

	docker cp "$work/database.dump" "$("${COMPOSE[@]}" ps -q postgres):/tmp/database.dump"
	"${COMPOSE[@]}" exec -T postgres pg_restore --no-owner --no-privileges \
		--dbname "postgres://invoice:${pgpass}@localhost:5432/invoice" /tmp/database.dump \
		|| warn "pg_restore reported errors — read them before trusting this restore"
	"${COMPOSE[@]}" exec -T postgres rm -f /tmp/database.dump
	ok "database restored"

	if [[ -d "$work/storage" ]]; then
		docker run --rm \
			-v "${COMPOSE_PROJECT}_storage:/dest" \
			-v "$work/storage:/src:ro" \
			alpine:3.20 sh -c 'cp -a /src/. /dest/' >/dev/null
		ok "issued PDFs, attachments and logos restored"
	else
		warn "archive contained no storage/ folder — there were no stored files when it was taken"
	fi
fi

# --------------------------------------------------------------------- run --

step "Building and starting the stack"

"${COMPOSE[@]}" up -d --build --remove-orphans
ok "containers started"

printf '  waiting for the application '
ready=""
for _ in $(seq 1 90); do
	if curl -sk "$BASE_URL/readyz" 2>/dev/null | grep -q '"ready":true'; then ready="yes"; break; fi
	printf '.'; sleep 2
done
printf '\n'
[[ -n "$ready" ]] || die "the application did not become ready. Run 'make logs' to see why."
ok "application is ready"

# --------------------------------------------------------------------- tls --

step "Certificate authority"

# Caddy signs with its own CA because a hosts-file name is not one a public
# authority could ever issue for. Trusting the root once is what makes the
# padlock mean something instead of being a warning to click past.
root_crt="$(mktemp -t caddy-root)"
"${COMPOSE[@]}" exec -T proxy cat /data/caddy/pki/authorities/local/root.crt > "$root_crt" 2>/dev/null \
	|| die "could not read Caddy's root certificate from the proxy container"

fingerprint="$(openssl x509 -in "$root_crt" -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2)"
if security find-certificate -a -Z /Library/Keychains/System.keychain 2>/dev/null | grep -qi "${fingerprint//:/}"; then
	skip "Caddy's CA already trusted"
else
	sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$root_crt"
	ok "Caddy's CA trusted"
fi
rm -f "$root_crt"

# ------------------------------------------------------------------ verify --

step "Verifying"

curl -s "$BASE_URL/readyz" | python3 -m json.tool | sed 's/^/  /'

gate="$("${COMPOSE[@]}" exec -T postgres psql -U invoice -d invoice -tAc \
	"SELECT value->>'real_issuance_enabled' FROM regulatory_config WHERE key='compliance_gate' ORDER BY effective_from DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')"

printf '\n'
if [[ "$gate" == "true" ]]; then
	ok "real issuance is ENABLED — documents issue into the official 'A' series"
else
	warn "real issuance is disabled: documents issue into the TEST series, watermarked"
	printf '    %sThat is the safe default. Open it with scripts/enable-real-issuance.sh%s\n' "$DIM" "$R"
fi

cat <<EOF

$B  Ready.$R  $BASE_URL

  Next:
    - log in, change the password if prompted
    - Settings → fill in the real business details, upload the logo
    - point your cloud sync at $BACKUP_DIR_HOST

  Day to day:
    make up / make down / make logs
    docs/RUNBOOK.md for restores and diagnosis

EOF
