#!/usr/bin/env bash
#
# deploy.sh — install or update a single-binary Econumo service on an Ubuntu VPS.
#
#   deploy.sh <project> <service> [<tag>] [MODE] [--purge-cache]
#
#   Default mode (no flag) is an update and needs <tag>. Exactly one MODE:
#
#   project       econumo | billing      selects the R2 prefix and binary name
#   service       systemd unit name      also the root of every path
#   tag           dev | latest | vX.Y.Z  the R2 channel (update only)
#   --install     first-time provisioning: install but do NOT start
#   --uninstall   remove the service, binary, env file and user; KEEPS the data
#                 directory unless --purge is also given
#   --purge       with --uninstall, also delete /var/lib/<service> (the database)
#   --start       start the service and wait for it to come up
#   --stop        stop the service
#   --restart     restart the service and wait for it to come up (no update)
#   --logs        follow the service journal (journalctl -f) until interrupted
#   --command ... run a management subcommand (e.g. currency:update-rates) with
#                 the service's env/user; MUST be last — the rest is passed through
#   --purge-cache after a successful update or uninstall, purge the whole
#                 Cloudflare zone cache (purge_everything). Runs last, so a
#                 failed/rolled-back deploy never purges. Not valid with
#                 --install/--restart/--command.
#
# Examples:
#   deploy.sh econumo econumo-beta dev --install
#   deploy.sh econumo econumo latest
#   deploy.sh billing econumo-billing latest
#   deploy.sh econumo econumo-beta dev --uninstall
#   deploy.sh econumo econumo-beta dev --uninstall --purge
#   deploy.sh econumo econumo latest --purge-cache
#   deploy.sh econumo econumo-test --start
#   deploy.sh econumo econumo-test --stop
#   deploy.sh econumo econumo-test --restart
#   deploy.sh econumo econumo-test --logs
#   deploy.sh econumo econumo-test --command currency:update-rates
#   deploy.sh econumo econumo-test --command user:create "John" john@x.test secret
#
# Paths (per service, so several instances coexist on one host):
#   binary  /opt/<service>/<project>          previous: <project>.prev
#   env     /etc/<service>/env
#   data    /var/lib/<service>
#   unit    /etc/systemd/system/<service>.service
#   user    <service> (system user, no login)
#
# Artifacts are pulled from s3://<bucket>/<project>/<tag>/ :
#   <project>-linux-{amd64,arm64} + SHA256SUMS
#
# Requires: root, the aws CLI, and R2 credentials in the environment:
#   ECONUMO_R2_ENDPOINT           https://<account_id>.r2.cloudflarestorage.com (required)
#   ECONUMO_R2_PROFILE            optional; a named ~/.aws profile, e.g. "r2"
#   ECONUMO_R2_ACCESS_KEY_ID      optional; explicit keys win over the profile
#   ECONUMO_R2_SECRET_ACCESS_KEY
#   ECONUMO_R2_BUCKET             optional, default "econumo"
#
# With no credential variables set, the aws CLI's own default resolution applies
# (the default profile, instance role, etc).
#
# --purge-cache additionally needs (curl is used, not the aws CLI):
#   ECONUMO_CF_ZONE_ID            Cloudflare zone id to purge
#   ECONUMO_CF_API_TOKEN          API token with the Zone > Cache Purge permission

set -euo pipefail

R2_BUCKET="${ECONUMO_R2_BUCKET:-econumo}"
HEALTH_TIMEOUT=10

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

# Poll systemd for HEALTH_TIMEOUT seconds; die with the tail of the log if the
# unit does not stay active. $1 is a verb for the failure message ("start" etc).
wait_until_active() {
	for _ in $(seq "$HEALTH_TIMEOUT"); do
		sleep 1
		systemctl is-active --quiet "$SERVICE" || {
			printf '\n--- last log lines ---\n' >&2
			journalctl -u "$SERVICE" -n 30 --no-pager >&2 || true
			die "$SERVICE failed to ${1:-come up}"
		}
	done
}

# Purge the whole Cloudflare zone cache (purge_everything). Credentials are
# validated in the preflight, so by the time this runs both vars are set.
purge_cloudflare_cache() {
	info "Purging Cloudflare cache (zone $ECONUMO_CF_ZONE_ID)"
	local response
	response="$(curl -fsS -X POST \
		"https://api.cloudflare.com/client/v4/zones/$ECONUMO_CF_ZONE_ID/purge_cache" \
		-H "Authorization: Bearer $ECONUMO_CF_API_TOKEN" \
		-H "Content-Type: application/json" \
		--data '{"purge_everything":true}' 2>/dev/null)" \
		|| die "Cloudflare purge request failed (network error or HTTP error status)"
	# The API returns 200 with {"success":false,...} for auth/permission problems,
	# so a passing curl is not enough — check the body.
	case "$response" in
		*'"success":true'*) info "Cloudflare cache purged" ;;
		*) die "Cloudflare purge rejected: $response" ;;
	esac
}

usage() {
	# Print the header comment block: everything from line 3 up to the first non-comment line.
	sed -n '3,${/^#/!q;s/^# \{0,1\}//p;}' "$0"
	exit "${1:-1}"
}

# ---------------------------------------------------------------- arguments

INSTALL=0
UNINSTALL=0
PURGE=0
PURGE_CACHE=0
START=0
STOP=0
RESTART=0
LOGS=0
RUN_COMMAND=0
CMD=()
ARGS=()
# Hand-roll the scan (not a for-in) so that everything after --command is captured
# verbatim as the CLI command and its args, flags included, without reparsing.
while [ "$#" -gt 0 ]; do
	case "$1" in
		--install) INSTALL=1 ;;
		--uninstall) UNINSTALL=1 ;;
		--purge) PURGE=1 ;;
		--purge-cache) PURGE_CACHE=1 ;;
		--start) START=1 ;;
		--stop) STOP=1 ;;
		--restart) RESTART=1 ;;
		--logs) LOGS=1 ;;
		--command)
			RUN_COMMAND=1
			shift
			CMD=("$@")   # the rest is the econumo subcommand + its args
			break
			;;
		-h|--help) usage 0 ;;
		-*) die "unknown flag: $1" ;;
		*) ARGS+=("$1") ;;
	esac
	shift
done
if [ "$RUN_COMMAND" -eq 1 ] && [ "${#CMD[@]}" -eq 0 ]; then
	die "--command requires a subcommand (e.g. --command currency:update-rates)"
fi

# Exactly one primary action. Update (the default) is the only one that fetches
# from R2 and thus needs the tag; the rest act on an already-installed service.
primary=$((INSTALL + UNINSTALL + START + STOP + RESTART + LOGS + RUN_COMMAND))
if [ "$primary" -gt 1 ]; then
	die "choose only one of --install / --uninstall / --start / --stop / --restart / --logs / --command"
fi

# --purge-cache is a modifier that runs after an update or an uninstall; it is
# never a mode on its own, and makes no sense with the lifecycle/command modes.
if [ "$PURGE_CACHE" -eq 1 ] && [ "$((START + STOP + RESTART + LOGS + RUN_COMMAND))" -gt 0 ]; then
	die "--purge-cache applies only to an update or --uninstall"
fi

# Only an update fetches from R2, so only it requires the tag; every other mode
# takes just <project> <service> and ignores a tag if one is given.
if [ "$((UNINSTALL + START + STOP + RESTART + LOGS + RUN_COMMAND))" -gt 0 ]; then
	[ "${#ARGS[@]}" -ge 2 ] && [ "${#ARGS[@]}" -le 3 ] || usage
else
	[ "${#ARGS[@]}" -eq 3 ] || usage
fi
# --install only provisions config (it never starts the service and predates a
# configured .env), so there is nothing new to serve — a cache purge is meaningless.
if [ "$INSTALL" -eq 1 ] && [ "$PURGE_CACHE" -eq 1 ]; then
	die "--purge-cache does not apply to --install (nothing is served yet)"
fi
# --purge only means anything as a modifier of --uninstall; a lone --purge is
# more likely a typo for --uninstall --purge than an intent to do nothing.
if [ "$PURGE" -eq 1 ] && [ "$UNINSTALL" -eq 0 ]; then
	die "--purge requires --uninstall"
fi

PROJECT="${ARGS[0]}"
SERVICE="${ARGS[1]}"
TAG="${ARGS[2]:-}"

case "$PROJECT" in
	econumo|billing) ;;
	*) die "project must be 'econumo' or 'billing' (got '$PROJECT')" ;;
esac

# The service name becomes a unix user, a unit name and three paths; keep it boring.
[[ "$SERVICE" =~ ^[a-z0-9][a-z0-9_-]*$ ]] \
	|| die "service name must be lowercase alphanumeric with - or _ (got '$SERVICE')"
if [ -n "$TAG" ] && ! [[ "$TAG" =~ ^[A-Za-z0-9._-]+$ ]]; then
	die "invalid tag: '$TAG'"
fi

BIN_DIR="/opt/$SERVICE"
BIN_PATH="$BIN_DIR/$PROJECT"
PREV_PATH="$BIN_PATH.prev"
ENV_DIR="/etc/$SERVICE"
ENV_FILE="$ENV_DIR/env"
DATA_DIR="/var/lib/$SERVICE"
UNIT_PATH="/etc/systemd/system/$SERVICE.service"

# ---------------------------------------------------------------- preflight

# Validate the Cloudflare inputs up front, whatever the mode, so a run that ends
# in a purge fails now rather than after all its other work has landed.
if [ "$PURGE_CACHE" -eq 1 ]; then
	command -v curl >/dev/null 2>&1 || die "curl is required for --purge-cache"
	[ -n "${ECONUMO_CF_ZONE_ID:-}" ] || die "--purge-cache needs ECONUMO_CF_ZONE_ID"
	[ -n "${ECONUMO_CF_API_TOKEN:-}" ] || die "--purge-cache needs ECONUMO_CF_API_TOKEN"
fi

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo $0 $*)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

# ------------------------------------------------ start/stop/restart/logs

if [ "$START" -eq 1 ] || [ "$STOP" -eq 1 ] || [ "$RESTART" -eq 1 ] || [ "$LOGS" -eq 1 ]; then
	[ -f "$UNIT_PATH" ] || die "$SERVICE is not installed ($UNIT_PATH missing)"
fi

if [ "$STOP" -eq 1 ]; then
	info "Stopping $SERVICE"
	systemctl stop "$SERVICE"
	info "$SERVICE stopped"
	exit 0
fi

if [ "$START" -eq 1 ]; then
	info "Starting $SERVICE"
	systemctl start "$SERVICE"
	wait_until_active "start"
	info "$SERVICE is running"
	exit 0
fi

if [ "$RESTART" -eq 1 ]; then
	info "Restarting $SERVICE"
	systemctl restart "$SERVICE"
	wait_until_active "come up after restart"
	info "$SERVICE is running"
	exit 0
fi

# --logs follows the journal until the user interrupts (Ctrl-C); exec so the
# signal goes straight to journalctl.
if [ "$LOGS" -eq 1 ]; then
	command -v journalctl >/dev/null 2>&1 || die "journalctl is required for --logs"
	exec journalctl -u "$SERVICE" -f
fi

# ------------------------------------------------------------ command mode
#
# Run a management subcommand (e.g. currency:update-rates, user:create) with the
# service's own EnvironmentFile, user and data directory, via a transient unit —
# so it sees the exact DATABASE_URL the service uses and writes the db as the
# service user. --pty forwards the terminal for interactive output.

if [ "$RUN_COMMAND" -eq 1 ]; then
	[ -x "$BIN_PATH" ] || die "$SERVICE is not installed ($BIN_PATH missing)"
	[ -f "$ENV_FILE" ] || die "$ENV_FILE missing — configure the service first"
	command -v systemd-run >/dev/null 2>&1 || die "systemd-run is required for --command"
	info "Running: $PROJECT ${CMD[*]} (as $SERVICE)"
	exec systemd-run --pty --quiet --wait --collect \
		--property=EnvironmentFile="$ENV_FILE" \
		--property=User="$SERVICE" \
		--property=WorkingDirectory="$DATA_DIR" \
		--property=ReadWritePaths="$DATA_DIR" \
		"$BIN_PATH" "${CMD[@]}"
fi

# ---------------------------------------------------------- uninstall mode
#
# Runs before the R2 preflight: tearing a service down needs no download, so it
# must work even without credentials or network. Every step tolerates a missing
# piece, so a half-finished install can still be cleaned up.

if [ "$UNINSTALL" -eq 1 ]; then
	found=0
	for p in "$UNIT_PATH" "$BIN_DIR" "$ENV_DIR" "$DATA_DIR"; do
		if [ -e "$p" ]; then found=1; fi
	done
	if [ "$found" -eq 0 ] && ! id -u "$SERVICE" >/dev/null 2>&1; then
		die "nothing to uninstall: no trace of '$SERVICE' on this host"
	fi

	if [ -e "$UNIT_PATH" ]; then
		info "Stopping and disabling $SERVICE"
		systemctl stop "$SERVICE" 2>/dev/null || true
		systemctl disable "$SERVICE" 2>/dev/null || true
		info "Removing $UNIT_PATH"
		rm -f "$UNIT_PATH"
		systemctl daemon-reload
		systemctl reset-failed "$SERVICE" 2>/dev/null || true
	fi

	if [ -e "$BIN_DIR" ]; then
		info "Removing $BIN_DIR"
		rm -rf "$BIN_DIR"
	fi

	if [ -e "$ENV_DIR" ]; then
		info "Removing $ENV_DIR"
		rm -rf "$ENV_DIR"
	fi

	if [ "$PURGE" -eq 1 ]; then
		if [ -e "$DATA_DIR" ]; then
			info "Purging $DATA_DIR (database included)"
			rm -rf "$DATA_DIR"
		fi
	elif [ -e "$DATA_DIR" ]; then
		info "Keeping $DATA_DIR (use --purge to delete it)"
	fi

	# Remove the user last: while the data directory survives it stays owned by
	# a live uid, so a later reinstall of the same service name still matches.
	if id -u "$SERVICE" >/dev/null 2>&1; then
		if [ "$PURGE" -eq 1 ] || [ ! -e "$DATA_DIR" ]; then
			info "Removing system user $SERVICE"
			userdel "$SERVICE" 2>/dev/null || true
		else
			info "Keeping user $SERVICE (it still owns $DATA_DIR)"
		fi
	fi

	printf '\n'
	if [ "$PURGE" -eq 1 ] || [ ! -e "$DATA_DIR" ]; then
		info "$SERVICE fully removed."
	else
		info "$SERVICE removed. Data kept at $DATA_DIR"
	fi
	if [ "$PURGE_CACHE" -eq 1 ]; then purge_cloudflare_cache; fi
	exit 0
fi

command -v aws >/dev/null 2>&1 || die "the aws CLI is required (apt install awscli, or the v2 installer)"
[ -n "${ECONUMO_R2_ENDPOINT:-}" ] \
	|| die "ECONUMO_R2_ENDPOINT is required (https://<account_id>.r2.cloudflarestorage.com)"

case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported architecture: $(uname -m)" ;;
esac

ASSET="$PROJECT-linux-$ARCH"
SRC="s3://$R2_BUCKET/$PROJECT/$TAG"

# aws CLI v2 sends integrity headers R2 rejects; only send them when required.
export AWS_REQUEST_CHECKSUM_CALCULATION=when_required
if [ -n "${ECONUMO_R2_ACCESS_KEY_ID:-}" ] && [ -n "${ECONUMO_R2_SECRET_ACCESS_KEY:-}" ]; then
	export AWS_ACCESS_KEY_ID="$ECONUMO_R2_ACCESS_KEY_ID"
	export AWS_SECRET_ACCESS_KEY="$ECONUMO_R2_SECRET_ACCESS_KEY"
elif [ -n "${ECONUMO_R2_PROFILE:-}" ]; then
	export AWS_PROFILE="$ECONUMO_R2_PROFILE"
fi

if [ "$INSTALL" -eq 0 ] && [ ! -f "$UNIT_PATH" ]; then
	die "$SERVICE is not installed ($UNIT_PATH missing) — run: $0 $PROJECT $SERVICE $TAG --install"
fi

# ---------------------------------------------------------- fetch and verify

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "Downloading $ASSET from $SRC"
aws s3 cp "$SRC/$ASSET" "$TMP/$ASSET" --endpoint-url "$ECONUMO_R2_ENDPOINT" --only-show-errors \
	|| die "download failed: $SRC/$ASSET"
aws s3 cp "$SRC/SHA256SUMS" "$TMP/SHA256SUMS" --endpoint-url "$ECONUMO_R2_ENDPOINT" --only-show-errors \
	|| die "download failed: $SRC/SHA256SUMS"

info "Verifying checksum"
# SHA256SUMS covers both architectures; check only the file we fetched.
grep -E "[[:space:]][*]?$ASSET\$" "$TMP/SHA256SUMS" > "$TMP/expected.sha256" \
	|| die "no checksum for $ASSET in SHA256SUMS"
( cd "$TMP" && sha256sum -c --status expected.sha256 ) \
	|| die "checksum mismatch for $ASSET — refusing to install"
chmod 0755 "$TMP/$ASSET"

# Nothing on disk has been touched up to this point.

# ------------------------------------------------------------ install mode

if [ "$INSTALL" -eq 1 ]; then
	if ! id -u "$SERVICE" >/dev/null 2>&1; then
		info "Creating system user $SERVICE"
		useradd --system --no-create-home --shell /usr/sbin/nologin --home-dir "$DATA_DIR" "$SERVICE"
	fi

	info "Creating directories"
	install -d -m 0755 "$BIN_DIR"
	install -d -m 0750 -o "$SERVICE" -g "$SERVICE" "$DATA_DIR"
	install -d -m 0750 "$ENV_DIR"

	if [ -e "$ENV_FILE" ]; then
		info "Keeping existing $ENV_FILE"
	else
		info "Creating empty $ENV_FILE"
		install -m 0640 -o root -g "$SERVICE" /dev/null "$ENV_FILE"
	fi

	info "Writing $UNIT_PATH"
	cat > "$UNIT_PATH" <<-UNIT
		[Unit]
		Description=$SERVICE ($PROJECT)
		After=network-online.target
		Wants=network-online.target

		[Service]
		Type=simple
		User=$SERVICE
		Group=$SERVICE
		WorkingDirectory=$DATA_DIR
		EnvironmentFile=$ENV_FILE
		ExecStart=$BIN_PATH serve
		Restart=on-failure
		RestartSec=5

		# Hardening: the process only ever writes its database directory.
		NoNewPrivileges=yes
		ProtectSystem=strict
		ProtectHome=yes
		PrivateTmp=yes
		ReadWritePaths=$DATA_DIR

		[Install]
		WantedBy=multi-user.target
	UNIT

	install -m 0755 "$TMP/$ASSET" "$BIN_PATH"
	systemctl daemon-reload
	systemctl enable "$SERVICE" >/dev/null

	cat <<-NEXT

	Installed $PROJECT ($TAG) as $SERVICE, not started.

	Next:
	  1. configure the environment:  \$EDITOR $ENV_FILE
	  2. start the service:          systemctl start $SERVICE
	  3. check it came up:           systemctl status $SERVICE

	NEXT
	exit 0
fi

# ------------------------------------------------------------- update mode

# Both binaries expose a bare `version` subcommand; only econumo also takes --version.
old_version="$("$BIN_PATH" version 2>/dev/null | head -n1 || true)"

info "Backing up current binary to $PREV_PATH"
cp -a "$BIN_PATH" "$PREV_PATH"

info "Installing $ASSET ($TAG)"
# install(1) writes to a temp file and renames, so the swap is atomic.
install -m 0755 "$TMP/$ASSET" "$BIN_PATH"

rollback() {
	printf '\n' >&2
	info "Rolling back to the previous binary" >&2
	install -m 0755 "$PREV_PATH" "$BIN_PATH"
	systemctl restart "$SERVICE" || true
	printf '\n--- last log lines ---\n' >&2
	journalctl -u "$SERVICE" -n 30 --no-pager >&2 || true
	die "$SERVICE failed to start on $TAG; rolled back to the previous binary"
}

info "Restarting $SERVICE"
systemctl restart "$SERVICE" || rollback

# systemd reports active immediately; give the process a moment to crash on its own.
for _ in $(seq "$HEALTH_TIMEOUT"); do
	sleep 1
	systemctl is-active --quiet "$SERVICE" || rollback
done

new_version="$("$BIN_PATH" version 2>/dev/null | head -n1 || true)"

info "$SERVICE is running"
if [ -n "$old_version" ] || [ -n "$new_version" ]; then
	printf '    %s -> %s\n' "${old_version:-unknown}" "${new_version:-$TAG}"
else
	printf '    now on channel %s\n' "$TAG"
fi

# The cache purge is the last thing to run, so a failed deploy never purges.
if [ "$PURGE_CACHE" -eq 1 ]; then purge_cloudflare_cache; fi
