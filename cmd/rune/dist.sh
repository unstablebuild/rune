#!/bin/bash
# Publish a Rune release artifact as a GitHub release asset.
#
# Required environment:
#   BLUE_RELEASE_TAR    - path to the artifact (.tar.gz or .dmg)
#   BLUE_TARGET_OS      - target OS (e.g. linux, darwin)
#   BLUE_TARGET_ARCH    - target arch (e.g. amd64, arm64)
#
# Optional:
#   RELEASE_REPO        - GitHub repo to publish to
#                         (default: unstablebuild/rune)
#   DOWNLOAD_HOST       - public origin clients resolve manifests from
#                         (default: https://github.com/<repo>/releases/latest/download)
#
# Requires the `gh` CLI, authenticated via `gh auth login` or GH_TOKEN.
set -e

RELEASE_REPO="${RELEASE_REPO:-unstablebuild/rune}"
DOWNLOAD_HOST="${DOWNLOAD_HOST:-https://github.com/${RELEASE_REPO}/releases/latest/download}"

# Reject anything that the in-product upgrader would refuse so we never
# publish a manifest that downgrade/version checks treat as garbage. The
# dist-* make rules also run this as their first prerequisite so the tag
# is validated before any build/notarize work. Sourcing here keeps the
# check authoritative even when dist.sh is invoked directly.
source "$(dirname "${BASH_SOURCE[0]}")/check-release-tag.sh"

# Download host must be https://; the client refuses anything else.
if [[ "$DOWNLOAD_HOST" != https://* ]]; then
    echo "ERROR: DOWNLOAD_HOST must be https://...; got '${DOWNLOAD_HOST}'. The in-product upgrader refuses non-HTTPS URLs."
    exit 1
fi

if [[ -z "${BLUE_RELEASE_TAR}" ]]; then
    echo "BLUE_RELEASE_TAR is not set. Pass the path to the release artifact."
    exit 1
fi

if [[ -z "${BLUE_TARGET_OS}" ]]; then
    echo "BLUE_TARGET_OS is not set. Pass the target OS (e.g. linux)."
    exit 1
fi

if [[ -z "${BLUE_TARGET_ARCH}" ]]; then
    echo "BLUE_TARGET_ARCH is not set. Pass the target arch (e.g. amd64)."
    exit 1
fi

# Final gate before publish: never ship .go source inside the artifact.
"$(dirname "${BASH_SOURCE[0]}")/../verify-no-go-source.sh" "$BLUE_RELEASE_TAR"

# Final gate before publish: never ship a macOS artifact whose minimum
# OS exceeds the floor we advertise. macOS enforces the binary's minos
# at launch, so a too-high floor (e.g. built on a newer SDK without
# pinning -mmacosx-version-min) makes the DMG refuse to open for users
# on supported releases. Fail-closed: a darwin publish without an
# explicit RUNE_MIN_MACOS is a configuration bug, not a reason to skip
# the check.
if [[ "$BLUE_TARGET_OS" == "darwin" ]]; then
    if [[ -z "${RUNE_MIN_MACOS}" ]]; then
        echo "ERROR: RUNE_MIN_MACOS is not set for a darwin publish — refusing to publish unverified." >&2
        echo "       The dist-darwin-* make rules set it from DARWIN_<arch>_MIN_MACOS." >&2
        exit 1
    fi
    "$(dirname "${BASH_SOURCE[0]}")/../verify-min-macos.sh" \
        "$BLUE_RELEASE_TAR" "$RUNE_MIN_MACOS"
fi

# Final gate before publish: never ship a Linux artifact that requires a
# glibc symbol version newer than the floor we advertise. The dynamic
# loader refuses to start such a binary on a floor host ("version
# `GLIBC_2.38' not found"), so a dependency built against a newer
# toolchain would silently break supported users. Fail-closed: a linux
# publish without the explicit floor is a configuration bug, not a
# reason to skip the check.
if [[ "$BLUE_TARGET_OS" == "linux" ]]; then
    if [[ -z "${RUNE_MIN_GLIBC}" ]]; then
        echo "ERROR: RUNE_MIN_GLIBC not set for a linux publish — refusing to publish unverified." >&2
        echo "       The dist-linux-* make rules set it from RUNE_MIN_GLIBC." >&2
        exit 1
    fi
    "$(dirname "${BASH_SOURCE[0]}")/../verify-min-linux.sh" \
        "$BLUE_RELEASE_TAR" "$RUNE_MIN_GLIBC"
fi

release_arch="${BLUE_TARGET_OS}-${BLUE_TARGET_ARCH}"

# Release asset names are flat — GitHub rejects a path separator — so the
# arch that used to be a bucket directory becomes part of the filename.
# `alias` is the unversioned name: because release assets are addressable
# as releases/latest/download/<name>, an unversioned asset is what gives
# the stable download link the old mutable "-latest" pointer provided.
case "$BLUE_RELEASE_TAR" in
	*.dmg)  asset="Rune-${GIT_TAG}-${release_arch}.dmg"
	        alias="Rune-${release_arch}.dmg" ;;
	*)      asset="rune-${GIT_TAG}-${release_arch}.tar.gz"
	        alias="rune-${release_arch}.tar.gz" ;;
esac
manifest_name="manifest-${release_arch}.json"

if ! command -v gh >/dev/null 2>&1; then
	echo "ERROR: the gh CLI is required to publish; see https://cli.github.com." >&2
	exit 1
fi

# Each arch publishes independently, so the draft may already exist from the
# Release workflow or a sibling dist-* run.
RELEASE_REPO="$RELEASE_REPO" "$(dirname "${BASH_SOURCE[0]}")/draft-release.sh"

stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/rune-dist-XXXXXX")"
cp "$BLUE_RELEASE_TAR" "${stage_dir}/${asset}"
cp "$BLUE_RELEASE_TAR" "${stage_dir}/${alias}"

echo "Uploading ${asset} and ${alias} to ${RELEASE_REPO}@${GIT_TAG} ..."
gh release upload "$GIT_TAG" \
	"${stage_dir}/${asset}" "${stage_dir}/${alias}" \
	--repo "$RELEASE_REPO" --clobber
rm -rf "$stage_dir"

# The manifest points at the immutable tagged asset rather than the
# latest/download alias so version and url can never disagree.
artifact_url="https://github.com/${RELEASE_REPO}/releases/download/${GIT_TAG}/${asset}"
echo "Public download URLs:"
echo "  ${artifact_url}"
echo "  ${DOWNLOAD_HOST}/${alias}"

# Compute the SHA256 of the artifact using whichever tool is available.
# `shasum -a 256` is shipped on macOS; `sha256sum` is the GNU utility on Linux.
if command -v shasum >/dev/null 2>&1; then
	artifact_sha256=$(shasum -a 256 "$BLUE_RELEASE_TAR" | awk '{print $1}')
elif command -v sha256sum >/dev/null 2>&1; then
	artifact_sha256=$(sha256sum "$BLUE_RELEASE_TAR" | awk '{print $1}')
else
	echo "ERROR: neither shasum nor sha256sum is available — cannot compute checksum."
	exit 1
fi
artifact_size=$(wc -c < "$BLUE_RELEASE_TAR" | tr -d ' ')
published_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Source changelog from the top of CHANGELOG.md when present. The "top
# section" is everything from the first H2 heading up to (but not
# including) the next H2 heading, similar to how release notes are
# typically extracted.
changelog_json="\"\""
repo_root=$(git rev-parse --show-toplevel 2>/dev/null || echo ".")
changelog_file="${repo_root}/CHANGELOG.md"
if [[ -f "$changelog_file" ]]; then
	# Extract the first H2 section (## ...) using awk, then JSON-encode it.
	changelog_text=$(awk '
		/^## / {
			if (seen) { exit }
			seen = 1
		}
		seen { print }
	' "$changelog_file")
	if [[ -n "$changelog_text" ]]; then
		# JSON-encode by escaping backslashes, double quotes, and newlines.
		changelog_json=$(printf '%s' "$changelog_text" | python3 -c '
import json, sys
sys.stdout.write(json.dumps(sys.stdin.read()))
' 2>/dev/null || echo '""')
	fi
fi

# Write the release manifest. Rune clients fetch this asset directly at:
#   ${DOWNLOAD_HOST}/${manifest_name}
# There is no server-side proxy; the file IS the manifest endpoint.
manifest_dir="$(mktemp -d "${TMPDIR:-/tmp}/rune-manifest-XXXXXX")"
manifest_tmp="${manifest_dir}/${manifest_name}"
cat >"$manifest_tmp" <<EOF
{
  "version": "${GIT_TAG}",
  "commit": "$(git rev-parse --short HEAD)",
  "os": "${BLUE_TARGET_OS}",
  "arch": "${BLUE_TARGET_ARCH}",
  "filename": "${asset}",
  "url": "${artifact_url}",
  "sha256": "${artifact_sha256}",
  "size": ${artifact_size},
  "published_at": "${published_at}",
  "changelog": ${changelog_json}
}
EOF

echo "Publishing manifest ${manifest_name} to ${RELEASE_REPO}@${GIT_TAG} ..."
gh release upload "$GIT_TAG" "$manifest_tmp" --repo "$RELEASE_REPO" --clobber
echo "  ${DOWNLOAD_HOST}/${manifest_name}"
rm -rf "$manifest_dir"
