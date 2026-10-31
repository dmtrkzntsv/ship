#!/usr/bin/env bash
# End-to-end test of ship on a real systemd host, against an in-memory S3
# server (test/e2e/fakes3). Run as root on a disposable host with systemd
# (CI: GitHub's ubuntu-latest via `sudo make e2e`); it needs go, jq and curl.
#
# It only touches names starting with e2e/old (users, services, units,
# /opt, /etc, /var/lib, /run, tmpfiles entries), ports 18080/18081 and 9000,
# refuses to start if any of those already exist, and removes everything it
# created when it exits, whether it passed or not.
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SHIP=$REPO/bin/ship
STATE=/var/lib/ship/state.json
UNITS=(e2e-sidecar e2e old)
USERS=(e2e old)

export SHIP_R2_ENDPOINT=http://127.0.0.1:9000
export SHIP_R2_ACCESS_KEY_ID=e2e
export SHIP_R2_SECRET_ACCESS_KEY=e2e-secret
export SHIP_R2_BUCKET=lab

log() { printf '\n== %s\n' "$*"; }
fail() {
	echo "e2e: FAIL: $*" >&2
	exit 1
}

# ship runs the binary under test, echoing the command to stderr so that
# captured output stays clean.
ship() {
	echo "+ ship $*" >&2
	"$SHIP" "$@"
}

# expect_fail runs ship and fails the test if it succeeds; the combined
# output is left in LAST_OUT.
LAST_OUT=
expect_fail() {
	echo "+ ship $* (must fail)" >&2
	if LAST_OUT=$("$SHIP" "$@" 2>&1); then
		echo "$LAST_OUT"
		fail "ship $* succeeded"
	fi
	echo "$LAST_OUT"
}

assert_eq() { [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"; }
assert_file() { [ -f "$1" ] || fail "$1 does not exist"; }
assert_dir() { [ -d "$1" ] || fail "directory $1 does not exist"; }
assert_gone() { [ ! -e "$1" ] || fail "$1 still exists"; }
assert_mode() { assert_eq "$(stat -c '%a %U:%G' "$1")" "$2" "mode and owner of $1"; }
assert_contains() { grep -qE -- "$2" <<<"$1" || fail "$3: output does not match /$2/"; }
assert_active() { systemctl is-active --quiet "$1" || fail "$1 is $(systemctl is-active "$1" || true), want active"; }
assert_not_active() { ! systemctl is-active --quiet "$1" || fail "$1 is active, want not active"; }
assert_user() { id -u "$1" >/dev/null 2>&1 || fail "user $1 does not exist"; }
assert_no_user() { ! id -u "$1" >/dev/null 2>&1 || fail "user $1 still exists"; }

# wait_active waits up to 10 s for a unit that was just (re)started.
wait_active() {
	for _ in $(seq 20); do
		systemctl is-active --quiet "$1" && return 0
		sleep 0.5
	done
	assert_active "$1"
}

# wait_served waits up to 10 s for the fixture on port $1 to answer $2.
wait_served() {
	local got=
	for _ in $(seq 20); do
		got=$(curl -fsS "http://127.0.0.1:$1/" 2>/dev/null || true)
		[ "$got" = "$2" ] && return 0
		sleep 0.5
	done
	fail "port $1 serves '$got', want '$2'"
}

# st reads a jq path from the state file.
st() { jq -r "$1" "$STATE"; }

# assert_running checks every view of the version service $1 runs on
# port $2: the installed binary, HTTP, the proof file and state.
assert_running() {
	local service=$1 port=$2 want=$3
	wait_active "$service"
	assert_eq "$("/opt/$service/fixture" version)" "$want" "/opt/$service/fixture version"
	wait_served "$port" "$want"
	assert_eq "$(cat "/var/lib/$service/served")" "$want" "/var/lib/$service/served"
	assert_eq "$(st ".services[\"$service\"].refs.fixture.version")" "$want" "state version of $service"
}

main_pid() { systemctl show -p MainPID --value "$1"; }

preflight() {
	[ "$(id -u)" -eq 0 ] || fail "run as root"
	for c in systemctl systemd-analyze systemd-run journalctl useradd userdel go jq curl; do
		command -v "$c" >/dev/null || fail "$c not found"
	done
	for u in "${USERS[@]}"; do
		! getent passwd "$u" >/dev/null || fail "user $u already exists; run on a disposable host"
		! getent group "$u" >/dev/null || fail "group $u already exists; run on a disposable host"
		for p in "/opt/$u" "/etc/$u" "/var/lib/$u" "/run/$u" "/etc/tmpfiles.d/ship-$u.conf"; do
			[ ! -e "$p" ] || fail "$p already exists; run on a disposable host"
		done
		if [ -f "$STATE" ] && [ "$(jq -r --arg s "$u" '.services[$s] != null' "$STATE")" = true ]; then
			fail "service $u is already in $STATE"
		fi
	done
	for unit in "${UNITS[@]}"; do
		[ ! -e "/etc/systemd/system/$unit.service" ] || fail "unit $unit.service already exists"
	done
	[ -x "$SHIP" ] || fail "$SHIP not built (run: make build)"
}

FAKES3_PID=
WORK=
MADE_STATE_DIR=false
MADE_CACHE_DIR=false

# cleanup removes everything the test may have created. It runs on every
# exit and tolerates any of it being absent already.
cleanup() {
	local status=$?
	set +e
	if [ "$status" -ne 0 ]; then
		echo "--- journal of the e2e units ---" >&2
		journalctl --no-pager -n 80 -u e2e -u e2e-sidecar -u old >&2
		[ -n "$WORK" ] && [ -f "$WORK/fakes3.log" ] && { echo "--- fakes3 log ---" >&2; tail -n 20 "$WORK/fakes3.log" >&2; }
	fi
	for unit in "${UNITS[@]}"; do
		systemctl stop "$unit" 2>/dev/null
		systemctl disable "$unit" 2>/dev/null
		rm -f "/etc/systemd/system/$unit.service"
	done
	systemctl daemon-reload
	for unit in "${UNITS[@]}"; do systemctl reset-failed "$unit" 2>/dev/null; done
	for u in "${USERS[@]}"; do
		rm -rf "/opt/${u:?}" "/etc/${u:?}" "/var/lib/${u:?}" "/run/${u:?}" "/etc/tmpfiles.d/ship-${u:?}.conf" \
			"/opt/${u:?}.pre-ship" "/etc/${u:?}.pre-ship"
		getent passwd "$u" >/dev/null && userdel "$u"
		getent group "$u" >/dev/null && groupdel "$u"
	done
	if [ "$MADE_STATE_DIR" = true ]; then
		rm -rf /var/lib/ship
	elif [ -f "$STATE" ]; then
		jq '.services |= del(.e2e, .old)' "$STATE" >"$STATE.e2e" && mv "$STATE.e2e" "$STATE"
	fi
	[ "$MADE_CACHE_DIR" = true ] && rm -rf /var/cache/ship
	if [ -n "$FAKES3_PID" ]; then
		kill "$FAKES3_PID" 2>/dev/null
		wait "$FAKES3_PID" 2>/dev/null
	fi
	[ -n "$WORK" ] && rm -rf "${WORK:?}"
	return "$status"
}

# build_release builds the fixture with version $2 (and extra ldflags $3)
# into a publish folder $WORK/$1 holding both assets, fixture and
# fixture-sidecar. Publishing needs amd64 and arm64 files; only the host's
# arch is ever run, so the host build is used for both names.
build_release() {
	local dir=$WORK/$1
	mkdir -p "$dir"
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$2 ${3:-}" -o "$WORK/fixture.bin" ./test/e2e/fixture
	for arch in amd64 arm64; do
		cp "$WORK/fixture.bin" "$dir/fixture-linux-$arch"
		cp "$WORK/fixture.bin" "$dir/fixture-sidecar-linux-$arch"
	done
	cp test/e2e/fixture/fixture.service test/e2e/fixture/fixture-sidecar.service test/e2e/fixture/fixture.env.example "$dir/"
}

start_fakes3() {
	go build -o "$WORK/fakes3" ./test/e2e/fakes3
	"$WORK/fakes3" -addr 127.0.0.1:9000 -bucket "$SHIP_R2_BUCKET" >"$WORK/fakes3.log" 2>&1 &
	FAKES3_PID=$!
	for _ in $(seq 50); do
		curl -s -o /dev/null http://127.0.0.1:9000/ && return 0
		kill -0 "$FAKES3_PID" 2>/dev/null || fail "fakes3 exited: $(cat "$WORK/fakes3.log")"
		sleep 0.2
	done
	fail "fakes3 did not come up"
}

cd "$REPO"
preflight
[ -d /var/lib/ship ] || MADE_STATE_DIR=true
[ -d /var/cache/ship ] || MADE_CACHE_DIR=true
trap cleanup EXIT
WORK=$(mktemp -d /tmp/ship-e2e.XXXXXX)
start_fakes3
ARCH=$(go env GOARCH)

log "1. build fixture v1, v2 (broken), v3; publish v1 to latest"
build_release v1 v1
build_release v2 v2 "-X main.broken=1"
build_release v3 v3
ship publish fixture --path "$WORK/v1" --version v1 --channels latest
out=$(ship versions fixture)
echo "$out"
assert_contains "$out" '^v1 +v1 ' "versions"
assert_contains "$out" '^latest +v1 ' "versions"

log "2. install fixture into service e2e: enabled, not started"
ship install fixture --service e2e --version latest
assert_not_active e2e
assert_eq "$(systemctl is-enabled e2e)" enabled "is-enabled e2e"
assert_file /etc/e2e/fixture.env
grep -qx 'PORT=' /etc/e2e/fixture.env || fail "env file was not seeded from fixture.env.example"
assert_mode /etc/e2e/fixture.env "640 root:e2e"
assert_mode /etc/e2e "750 root:e2e"
assert_mode /var/lib/e2e "750 e2e:e2e"
assert_mode /run/e2e "750 e2e:e2e"
assert_mode /opt/e2e/fixture "755 root:root"
assert_file /etc/tmpfiles.d/ship-e2e.conf
assert_user e2e
assert_eq "$(st '.services.e2e.refs.fixture | "\(.unit) \(.channel) \(.version)"')" "e2e latest v1" "state of e2e"
assert_eq "$(st '.services.e2e.created_user')" true "created_user of e2e"
assert_mode "$STATE" "644 root:root"
printf 'PORT=18080\n' >/etc/e2e/fixture.env
ship start fixture --service e2e
assert_running e2e 18080 v1
assert_file /run/e2e/e2e.ready

log "3. a broken update is rolled back and v1 keeps running"
ship publish fixture --path "$WORK/v2" --version v2 --channels latest
expect_fail update fixture --service e2e
assert_contains "$LAST_OUT" 'previous version restored' "failed update"
# The broken build really ran: its error reaches the journal (journald may
# lag behind the failed health check, so poll briefly).
for _ in $(seq 20); do
	journalctl --no-pager -u e2e | grep 'v2 is a deliberately broken build' >/dev/null && break
	sleep 0.5
done
journalctl --no-pager -u e2e | grep 'v2 is a deliberately broken build' >/dev/null ||
	fail "the journal of e2e has no error from the broken v2 build"
assert_running e2e 18080 v1
assert_gone /opt/e2e/fixture.prev

log "4. update to v3, roll back to v1, update to v3 again"
ship publish fixture --path "$WORK/v3" --version v3 --channels latest
ship update fixture --service e2e
assert_running e2e 18080 v3
assert_eq "$(st '.services.e2e.refs.fixture.prev_version')" v1 "prev_version after update"
assert_eq "$(/opt/e2e/fixture.prev version)" v1 "/opt/e2e/fixture.prev version"
assert_file /opt/e2e/e2e.service.prev
ship rollback fixture --service e2e
assert_running e2e 18080 v1
assert_eq "$(st '.services.e2e.refs.fixture.prev_version')" v3 "prev_version after rollback"
ship update fixture --service e2e
assert_running e2e 18080 v3

log "5. a second ref (component fixture/sidecar) shares the service"
ship install fixture/sidecar --service e2e --version latest
assert_eq "$(st '.services.e2e.refs["fixture/sidecar"].unit')" e2e-sidecar "unit of fixture/sidecar"
assert_file /etc/systemd/system/e2e-sidecar.service
assert_file /opt/e2e/fixture-sidecar
assert_file /etc/e2e/sidecar.env
[ ! -s /etc/e2e/sidecar.env ] || fail "/etc/e2e/sidecar.env should be empty (no env example published)"
assert_mode /etc/e2e/sidecar.env "640 root:e2e"
assert_not_active e2e-sidecar
pid=$(main_pid e2e)
ship start fixture/sidecar --service e2e
assert_active e2e-sidecar
assert_file /run/e2e/e2e-sidecar.ready
ship restart fixture/sidecar --service e2e
assert_active e2e-sidecar
assert_eq "$(main_pid e2e)" "$pid" "MainPID of e2e (a sibling must not be restarted)"
systemctl daemon-reexec
wait_active e2e
wait_active e2e-sidecar
assert_mode /run/e2e "750 e2e:e2e"
assert_file /run/e2e/e2e.ready
assert_file /run/e2e/e2e-sidecar.ready

log "6. run and status"
out=$(ship run fixture --service e2e -- echo hi </dev/null | tr -d '\r')
echo "$out"
grep -qx 'hi' <<<"$out" || fail "ship run printed '$out', want 'hi'"
out=$(ship status)
echo "$out"
assert_contains "$out" '^e2e +fixture +e2e +latest +v3 +active$' "status"
assert_contains "$out" '^e2e +fixture/sidecar +e2e-sidecar +latest +v3 +active$' "status"

log "7. publishing 12 more versions with --keep 10 prunes the oldest"
for i in $(seq 4 15); do
	build_release "v$i" "v$i"
	ship publish fixture --path "$WORK/v$i" --version "v$i" --channels latest --keep 10
done
out=$(ship versions fixture)
echo "$out"
# latest points at v15, which is protected and does not count toward the
# 10 kept: v5..v14 plus v15 remain, v1..v4 are pruned.
versions=$(awk 'NR > 1 && $1 != "latest" { print $1 }' <<<"$out" | sort -V | tr '\n' ' ')
assert_eq "$versions" "v5 v6 v7 v8 v9 v10 v11 v12 v13 v14 v15 " "version channels after pruning"
assert_contains "$out" '^latest +v15 ' "versions"
# Pruning never affects what is installed.
assert_running e2e 18080 v3

log "8. existing paths are not taken over; a hand-installed service moves to ship with the README recipe"
install -d -m 0755 /etc/old
expect_fail install fixture --service old --version latest
assert_contains "$LAST_OUT" "won't take over existing paths" "install over an existing /etc/old"
assert_gone /opt/old
assert_gone /var/lib/old
assert_no_user old
rmdir /etc/old
useradd --system --user-group --no-create-home --shell /usr/sbin/nologin --home-dir /var/lib/old old
install -d -m 0755 /opt/old
install -m 0755 "$WORK/v1/fixture-linux-$ARCH" /opt/old/fixture
install -d -m 0750 -o old -g old /var/lib/old
install -d -m 0750 /etc/old
install -m 0640 -g old /dev/null /etc/old/env
printf 'PORT=18081\n' >/etc/old/env
cat >/etc/systemd/system/old.service <<'EOF'
[Unit]
Description=ship e2e legacy service (old deploy script layout)

[Service]
User=old
Group=old
EnvironmentFile=/etc/old/env
Environment=DATA=/var/lib/old
WorkingDirectory=/var/lib/old
ExecStart=/opt/old/fixture serve

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now old
wait_active old
wait_served 18081 v1
assert_file /var/lib/old/served
expect_fail install fixture --service old --version latest
assert_contains "$LAST_OUT" "won't take over existing paths" "install over the old layout"
# the recipe: stop the old unit, move its files aside, install, restore the config
systemctl disable --now old
rm /etc/systemd/system/old.service
systemctl daemon-reload
mv /etc/old /etc/old.pre-ship
mv /opt/old /opt/old.pre-ship
ship install fixture --service old --version latest
cat /etc/old.pre-ship/env >/etc/old/fixture.env
ship start fixture --service old
assert_running old 18081 v15
assert_mode /etc/old/fixture.env "640 root:old"
assert_mode /etc/old "750 root:old"
assert_file /var/lib/old/served
assert_mode /run/old "750 old:old"
assert_file /run/old/old.ready
assert_eq "$(st '.services.old.refs.fixture | "\(.unit) \(.channel)"')" "old latest" "state of old"
assert_eq "$(st '.services.old.created_user // false')" false "created_user of old (the user existed)"
rm -rf /etc/old.pre-ship /opt/old.pre-ship

log "9. uninstall: the sidecar, then fixture (config, data and user kept), reinstall, then --purge"
ship uninstall fixture/sidecar --service e2e
assert_gone /etc/systemd/system/e2e-sidecar.service
assert_gone /opt/e2e/fixture-sidecar
assert_file /etc/e2e/sidecar.env
assert_not_active e2e-sidecar
assert_active e2e
assert_eq "$(st '.services.e2e.refs["fixture/sidecar"]')" null "state of fixture/sidecar"
ship uninstall fixture --service e2e
assert_not_active e2e
for p in /etc/systemd/system/e2e.service /opt/e2e /run/e2e /etc/tmpfiles.d/ship-e2e.conf; do
	assert_gone "$p"
done
assert_file /var/lib/e2e/served
assert_mode /etc/e2e "750 root:e2e"
grep -qx 'PORT=18080' /etc/e2e/fixture.env || fail "uninstall without --purge lost the env file"
assert_user e2e
assert_eq "$(st '.services.e2e')" null "state of e2e"
# a plain uninstall keeps /etc/e2e, and a new install picks it up again
ship install fixture --service e2e --version latest
grep -qx 'PORT=18080' /etc/e2e/fixture.env || fail "reinstall replaced the kept env file"
ship uninstall fixture --service e2e --purge
for p in /etc/systemd/system/e2e.service /opt/e2e /etc/e2e /run/e2e /var/lib/e2e /etc/tmpfiles.d/ship-e2e.conf; do
	assert_gone "$p"
done
# the reinstall found user e2e already there, so ship no longer counts it as its own
assert_user e2e
ship uninstall fixture --service old --purge
assert_not_active old
for p in /etc/systemd/system/old.service /opt/old /etc/old /run/old /var/lib/old /etc/tmpfiles.d/ship-old.conf; do
	assert_gone "$p"
done
assert_user old # it existed before ship
assert_eq "$(st '.services.old')" null "state of old"
if [ "$MADE_STATE_DIR" = true ]; then
	assert_contains "$(ship status)" '^no services installed$' "status after uninstall"
fi

log "cleanup"
trap - EXIT
cleanup
set -e
assert_no_user e2e
assert_gone /var/lib/e2e
echo "e2e: OK"
