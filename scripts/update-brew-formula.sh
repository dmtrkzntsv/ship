#!/usr/bin/env bash
# Render Formula/ship.rb for the homebrew tap and push it.
#
# Usage: update-brew-formula.sh <version> <dist-dir> [--dry-run]
#   <version>   release tag, e.g. v1.2.3
#   <dist-dir>  directory with ship-<version>-<os>-<arch>.tar.gz and SHA256SUMS
#   --dry-run   print the formula, do not touch the tap
#
# Environment:
#   TAP_GITHUB_TOKEN   token with contents:write on the tap (required unless --dry-run)
#   TAP_REPO           default dmtrkzntsv/homebrew-tap
#   GITHUB_REPOSITORY  default dmtrkzntsv/ship (used for download URLs)

set -euo pipefail

usage() { echo "usage: $0 <version> <dist-dir> [--dry-run]" >&2; exit 2; }

[[ $# -ge 2 && $# -le 3 ]] || usage
TAG="$1"
DIST="$2"
DRY=0
if [[ $# -eq 3 ]]; then
    [[ "$3" == "--dry-run" ]] || usage
    DRY=1
fi

[[ "$TAG" =~ ^v[0-9A-Za-z._-]+$ ]] || { echo "version must be a tag like v1.2.3, got: $TAG" >&2; exit 2; }
VERSION="${TAG#v}"
TAP_REPO="${TAP_REPO:-dmtrkzntsv/homebrew-tap}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-dmtrkzntsv/ship}"
if [[ $DRY -eq 0 ]]; then
    : "${TAP_GITHUB_TOKEN:?required}"
fi

[[ -f "$DIST/SHA256SUMS" ]] || { echo "missing $DIST/SHA256SUMS" >&2; exit 1; }

sha_for() {
    local archive="ship-${TAG}-$1.tar.gz" sum
    if [[ ! -f "$DIST/$archive" ]]; then
        echo "missing archive: $archive" >&2
        exit 1
    fi
    sum=$(awk -v f="$archive" '$2 == f { print $1 }' "$DIST/SHA256SUMS")
    if [[ -z "$sum" ]]; then
        echo "$archive is not listed in SHA256SUMS" >&2
        exit 1
    fi
    printf '%s' "$sum"
}

SHA_DARWIN_ARM64=$(sha_for darwin-arm64)
SHA_DARWIN_AMD64=$(sha_for darwin-amd64)
SHA_LINUX_AMD64=$(sha_for linux-amd64)
SHA_LINUX_ARM64=$(sha_for linux-arm64)

BASE_URL="https://github.com/${GITHUB_REPOSITORY}/releases/download/${TAG}"

render() {
    cat <<RUBY
class Ship < Formula
  desc "Publish and run lab services from R2"
  homepage "https://github.com/${GITHUB_REPOSITORY}"
  version "${VERSION}"

  on_macos do
    on_arm do
      url "${BASE_URL}/ship-${TAG}-darwin-arm64.tar.gz"
      sha256 "${SHA_DARWIN_ARM64}"
    end
    on_intel do
      url "${BASE_URL}/ship-${TAG}-darwin-amd64.tar.gz"
      sha256 "${SHA_DARWIN_AMD64}"
    end
  end

  on_linux do
    on_arm do
      url "${BASE_URL}/ship-${TAG}-linux-arm64.tar.gz"
      sha256 "${SHA_LINUX_ARM64}"
    end
    on_intel do
      url "${BASE_URL}/ship-${TAG}-linux-amd64.tar.gz"
      sha256 "${SHA_LINUX_AMD64}"
    end
  end

  def install
    bin.install "ship"
    generate_completions_from_executable(bin/"ship", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/ship version")
  end
end
RUBY
}

if [[ $DRY -eq 1 ]]; then
    render
    exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git clone "https://x-access-token:${TAP_GITHUB_TOKEN}@github.com/${TAP_REPO}.git" "$WORK/tap"
mkdir -p "$WORK/tap/Formula"
render > "$WORK/tap/Formula/ship.rb"

cd "$WORK/tap"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git add Formula/ship.rb
if git diff --cached --quiet; then
    echo "update-brew-formula: no changes to formula, skipping push"
    exit 0
fi
git commit -m "ship ${VERSION}"
git push origin HEAD

echo "update-brew-formula: pushed Formula/ship.rb to ${TAP_REPO}"
