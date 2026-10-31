#!/bin/sh
# Runs install.sh in an ubuntu:24.04 container against a local HTTP server
# serving a fake release directory. Needs docker and go.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

case "$(uname -m)" in
	x86_64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) echo "unsupported host arch" >&2; exit 1 ;;
esac

mkdir -p "$WORK/rel/latest/download"
REL="$WORK/rel/latest/download"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build \
	-ldflags "-X github.com/dmtrkzntsv/ship/internal/cli.Version=v9.9.9" \
	-o "$REL/ship-linux-$ARCH" ./cmd/ship)
cp "$ROOT/config.example.toml" "$REL/config.example.toml"
(cd "$REL" && sha256sum "ship-linux-$ARCH" >SHA256SUMS)
cp "$ROOT/install.sh" "$WORK/install.sh"

cat >"$WORK/inside.sh" <<INNER
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq curl python3 >/dev/null
mkdir -p /etc/bash_completion.d
(cd /srv/rel && python3 -m http.server 8080 >/dev/null 2>&1) &
sleep 1
export SHIP_BASE_URL=http://127.0.0.1:8080

fail() { echo "FAIL: \$*" >&2; exit 1; }

# 1. tampered checksums: must fail and install nothing
cp /srv/rel/latest/download/SHA256SUMS /tmp/SHA256SUMS.good
sed 's/^./x/' /tmp/SHA256SUMS.good >/srv/rel/latest/download/SHA256SUMS
if sh /work/install.sh; then fail "tampered SHA256SUMS was accepted"; fi
[ ! -e /usr/local/bin/ship ] || fail "binary installed despite checksum mismatch"
cp /tmp/SHA256SUMS.good /srv/rel/latest/download/SHA256SUMS

# 2. good install
sh /work/install.sh
[ "\$(/usr/local/bin/ship version)" = "ship v9.9.9" ] || fail "unexpected version output"
[ -f /etc/ship/config.toml ] || fail "config.toml missing"
[ "\$(stat -c %a /etc/ship/config.toml)" = 600 ] || fail "config.toml mode is not 600"
[ -s /etc/bash_completion.d/ship ] || fail "bash completion missing"

# 3. wget path: remove curl, reinstall
apt-get install -y -qq wget >/dev/null
rm -f "\$(command -v curl)"
! command -v curl >/dev/null 2>&1 || fail "curl still present"
rm -f /usr/local/bin/ship
sh /work/install.sh
[ -x /usr/local/bin/ship ] || fail "wget fallback did not install"
echo "install_test: OK"
INNER

docker run --rm -v "$WORK/rel:/srv/rel" -v "$WORK/install.sh:/work/install.sh:ro" \
	-v "$WORK/inside.sh:/work/inside.sh:ro" ubuntu:24.04 sh /work/inside.sh
