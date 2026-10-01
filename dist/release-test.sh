#!/bin/bash
# Regression test for dist/release.sh (the report printed after every prod dist
# and the rules for publishing the Homebrew cask) and cmd/rune/draft-release.sh
# (every release starts as a draft).
#
# Fake gh and curl serve fixture release assets, manifests and a tap cask, and
# record any publish attempt, so the test needs no network or credentials.
set -u

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-test-XXXXXX")"
trap 'rm -rf "$work"' EXIT

fakebin="$work/fakebin"
mkdir -p "$fakebin" "$work/manifests"
export ASSETS_FILE="$work/assets" TAP_CASK_FILE="$work/tap-cask.rb" \
	PUT_LOG="$work/put.log" MANIFEST_DIR="$work/manifests" CREATE_LOG="$work/create.log"

cat >"$fakebin/gh" <<'EOF'
#!/bin/bash
if [ "$1 $2" = "release create" ]; then
	printf '%s\n' "$@" >"$CREATE_LOG"
	exit 0
fi
if [ "$1" = release ]; then
	[ -f "$ASSETS_FILE" ] || { echo "release not found" >&2; exit 1; }
	echo "${RELEASE_DRAFT:-false}"
	echo "https://github.com/unstablebuild/rune/releases/tag/untagged-0123abc"
	cat "$ASSETS_FILE"
	exit 0
fi
if [ "$1 $2" = "api -X" ]; then
	printf '%s\n' "$@" >"$PUT_LOG"
	echo "https://github.com/unstablebuild/homebrew-rune/commit/0123abc"
	exit 0
fi
if [ "$1" = api ]; then
	[ -f "$TAP_CASK_FILE" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
	case "$*" in
	*"--jq .sha"*) echo deadbeef ;;
	*) cat "$TAP_CASK_FILE" ;;
	esac
	exit 0
fi
echo "fake gh: unexpected call: $*" >&2
exit 99
EOF

cat >"$fakebin/curl" <<'EOF'
#!/bin/bash
for url; do :; done
cat "$MANIFEST_DIR/$(basename "$url")" 2>/dev/null || exit 22
EOF
chmod +x "$fakebin/gh" "$fakebin/curl"

for platform in darwin-arm64 darwin-amd64 linux-arm64 linux-amd64; do
	sha=$(printf '%s' "$platform" | shasum -a 256 | cut -d' ' -f1)
	printf '{"version": "v1.3.0", "sha256": "%s"}\n' "$sha" \
		>"$work/manifests/manifest-$platform.json"
done

all_artifacts="Rune-v1.3.0-darwin-arm64.dmg
manifest-darwin-arm64.json
Rune-v1.3.0-darwin-amd64.dmg
manifest-darwin-amd64.json
rune-v1.3.0-linux-arm64.tar.gz
manifest-linux-arm64.json
rune-v1.3.0-linux-amd64.tar.gz
manifest-linux-amd64.json"
debs="rune_1.3.0_amd64.deb
rune_1.3.0_arm64.deb"

PATH="$fakebin:$PATH" RELEASE_TAG=v1.3.0 "$repo_root/dist/homebrew/update-cask.sh" \
	>"$work/current.rb" || { echo "FAIL: update-cask.sh could not generate the cask" >&2; exit 1; }
sed -e 's/^  version ".*"/  version "1.2.1"/' "$work/current.rb" >"$work/older.rb"
sed -e 's/^  version ".*"/  version "1.4.0"/' "$work/current.rb" >"$work/newer.rb"
sed -e 's/^  desc ".*"/  desc "Stale description"/' "$work/current.rb" >"$work/outdated.rb"

failures=0
fail() {
	echo "FAIL: $case_name: $*" >&2
	failures=$((failures + 1))
}

# scenario <name> <assets> <tap cask fixture or ""> <tag> <command> [draft]
scenario() {
	case_name=$1
	printf '%s\n' "$2" >"$ASSETS_FILE"
	rm -f "$TAP_CASK_FILE" "$PUT_LOG"
	[ -n "$3" ] && cp "$3" "$TAP_CASK_FILE"
	out=$(PATH="$fakebin:$PATH" RELEASE_DRAFT="${6:-false}" RELEASE_TAG="$4" \
		"$repo_root/dist/release.sh" "$5" 2>&1)
	rc=$?
}

expect_rc() { [ "$rc" -eq "$1" ] || fail "exit $rc, want $1; output:"$'\n'"$out"; }
expect_line() {
	printf '%s\n' "$out" | grep -qxF -- "$1" || fail "missing line: $1"$'\n'"output:"$'\n'"$out"
}
expect_published() {
	[ -f "$PUT_LOG" ] || { fail "nothing was published"; return; }
	grep -qxF "message=rune 1.3.0" "$PUT_LOG" || fail "commit message is not 'rune 1.3.0'"
	sed -n 's/^content=//p' "$PUT_LOG" | base64 --decode >"$work/put.rb"
	diff -q "$work/current.rb" "$work/put.rb" >/dev/null || fail "published cask differs from update-cask.sh"
}
expect_not_published() { [ ! -f "$PUT_LOG" ] || fail "published although it must not"; }

scenario "status after the first dist into the draft" \
	"Rune-v1.3.0-darwin-arm64.dmg
manifest-darwin-arm64.json" "$work/older.rb" v1.3.0 status true
expect_rc 0
want="
Release v1.3.0 (unstablebuild/rune, draft):
❌ GitHub release linux/arm64 .tar.gz
❌ GitHub release linux/amd64 .tar.gz
❌ GitHub release darwin/amd64 .dmg
❌ GitHub release linux/arm64 .deb
❌ GitHub release linux/amd64 .deb
❌ Homebrew */* .rb
✅ GitHub release darwin/arm64 .dmg

Linux artifacts come from the Release workflow, which runs when the tag is pushed: https://github.com/unstablebuild/rune/actions/workflows/release.yml
macOS artifacts are signed and attached from a Mac: make rune-prod-dist-darwin-<arch>
Homebrew release (v1.3.0) is missing but requires artifacts."
[ "$out" = "$want" ] || fail "report differs:"$'\n'"$(diff <(echo "$want") <(echo "$out"))"

scenario "status once the draft has every artifact" "$all_artifacts
$debs" "$work/older.rb" v1.3.0 status true
expect_rc 0
want="
Release v1.3.0 (unstablebuild/rune, draft):
❌ Homebrew */* .rb
✅ GitHub release linux/arm64 .tar.gz
✅ GitHub release linux/amd64 .tar.gz
✅ GitHub release darwin/amd64 .dmg
✅ GitHub release darwin/arm64 .dmg
✅ GitHub release linux/arm64 .deb
✅ GitHub release linux/amd64 .deb

Release v1.3.0 is a draft AND READY TO BE TESTED. Publish it once tested at https://github.com/unstablebuild/rune/releases/tag/untagged-0123abc
Homebrew release (v1.3.0) is missing but requires the release to be published."
[ "$out" = "$want" ] || fail "report differs:"$'\n'"$(diff <(echo "$want") <(echo "$out"))"

scenario "status once the release is published" "$all_artifacts
$debs" "$work/older.rb" v1.3.0 status
expect_rc 0
want="
Release v1.3.0 (unstablebuild/rune):
❌ Homebrew */* .rb
✅ GitHub release linux/arm64 .tar.gz
✅ GitHub release linux/amd64 .tar.gz
✅ GitHub release darwin/amd64 .dmg
✅ GitHub release darwin/arm64 .dmg
✅ GitHub release linux/arm64 .deb
✅ GitHub release linux/amd64 .deb

Homebrew release (v1.3.0) is missing AND READY TO BE PUBLISHED. Publish now with

make rune-prod-dist-homebrew"
[ "$out" = "$want" ] || fail "report differs:"$'\n'"$(diff <(echo "$want") <(echo "$out"))"
expect_not_published

scenario "status reports each .deb" "$all_artifacts
rune_1.3.0-1_amd64.deb" "$work/older.rb" v1.3.0 status
expect_line "❌ GitHub release linux/arm64 .deb"
expect_line "✅ GitHub release linux/amd64 .deb"
expect_line "Linux artifacts come from the Release workflow, which runs when the tag is pushed: https://github.com/unstablebuild/rune/actions/workflows/release.yml"
! printf '%s\n' "$out" | grep -q "macOS artifacts" || fail "macOS hint without a missing .dmg"

scenario "status does not call a draft missing only its .debs ready" "$all_artifacts" \
	"$work/older.rb" v1.3.0 status true
! printf '%s\n' "$out" | grep -q "READY TO BE TESTED" || fail "draft reported ready without its .debs"

scenario "status counts an artifact without its manifest as missing" \
	"$(printf '%s\n' "$all_artifacts" | grep -v manifest-darwin-amd64)" "$work/older.rb" v1.3.0 status
expect_line "❌ GitHub release darwin/amd64 .dmg"
expect_line "Homebrew release (v1.3.0) is missing but requires artifacts."

scenario "status counts a manifest without its artifact as missing" \
	"$(printf '%s\n' "$all_artifacts" | grep -v rune-v1.3.0-linux-arm64.tar.gz)" "$work/older.rb" v1.3.0 status
expect_line "❌ GitHub release linux/arm64 .tar.gz"
expect_line "Homebrew release (v1.3.0) is missing but requires artifacts."

scenario "status when the tap already has the cask" "$all_artifacts" "$work/current.rb" v1.3.0 status
expect_line "✅ Homebrew */* .rb"
expect_line "Homebrew release (v1.3.0) is published."

scenario "status when the tap has an older cask for the version" \
	"$all_artifacts" "$work/outdated.rb" v1.3.0 status
expect_line "❌ Homebrew */* .rb"
expect_line "Homebrew release (v1.3.0) is outdated AND READY TO BE PUBLISHED. Publish now with"

scenario "status when the tap ships a newer version" "$all_artifacts" "$work/newer.rb" v1.3.0 status
expect_line "➖ Homebrew */* .rb"
expect_line "Homebrew release (v1.3.0) is superseded: unstablebuild/homebrew-rune already ships v1.4.0."

scenario "status for a prerelease" "Rune-v1.3.0-rc.1-darwin-arm64.dmg" "$work/older.rb" v1.3.0-rc.1 status
expect_rc 0
expect_line "➖ Homebrew */* .rb"
expect_line "Homebrew release (v1.3.0-rc.1) is not applicable: Homebrew only ships stable releases."

scenario "publish without every artifact" \
	"Rune-v1.3.0-darwin-arm64.dmg
manifest-darwin-arm64.json" "$work/older.rb" v1.3.0 publish-homebrew
expect_rc 1
expect_line "Nothing was published."
expect_not_published

scenario "publish a draft" "$all_artifacts
$debs" "$work/older.rb" v1.3.0 publish-homebrew true
expect_rc 1
expect_line "Nothing was published."
expect_not_published

scenario "publish when ready" "$all_artifacts" "$work/older.rb" v1.3.0 publish-homebrew
expect_rc 0
expect_line "Homebrew release (v1.3.0) is published: https://github.com/unstablebuild/homebrew-rune/commit/0123abc"
expect_published
grep -qxF "sha=deadbeef" "$PUT_LOG" 2>/dev/null || fail "update did not send the current file's sha"

scenario "publish into a tap without the cask" "$all_artifacts" "" v1.3.0 publish-homebrew
expect_rc 0
expect_published
! grep -q "^sha=" "$PUT_LOG" 2>/dev/null || fail "creating the cask must not send a sha"

scenario "publish when already published" "$all_artifacts" "$work/current.rb" v1.3.0 publish-homebrew
expect_rc 0
expect_line "Homebrew release (v1.3.0) is published."
expect_not_published

scenario "publish over a newer tap" "$all_artifacts" "$work/newer.rb" v1.3.0 publish-homebrew
expect_rc 1
expect_not_published

scenario "publish a prerelease" "Rune-v1.3.0-rc.1-darwin-arm64.dmg" "$work/older.rb" v1.3.0-rc.1 publish-homebrew
expect_rc 1
expect_not_published

# draft-release.sh reads the tag at HEAD, so it runs inside a tagged repo.
tagged_repo="$work/tagged"
(
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
	git init -q "$tagged_repo" && cd "$tagged_repo" &&
		git -c user.email=t@example.com -c user.name=t commit -q --allow-empty -m seed &&
		git tag v1.3.0
) || { echo "FAIL: could not seed the tagged repo" >&2; exit 1; }
draft() {
	case_name=$1
	rm -f "$CREATE_LOG"
	out=$(cd "$tagged_repo" && PATH="$fakebin:$PATH" "$repo_root/cmd/rune/draft-release.sh" 2>&1)
	rc=$?
}

rm -f "$ASSETS_FILE"
draft "draft-release creates a missing release as a draft"
expect_rc 0
grep -qxF -- --draft "$CREATE_LOG" 2>/dev/null || fail "release was not created as a draft"
grep -qxF -- --verify-tag "$CREATE_LOG" 2>/dev/null || fail "release was created without --verify-tag"

printf '%s\n' "$all_artifacts" >"$ASSETS_FILE"
draft "draft-release keeps an existing release"
expect_rc 0
[ ! -f "$CREATE_LOG" ] || fail "created a release that already exists"

if [ "$failures" -gt 0 ]; then
	echo "release-test: $failures failure(s)" >&2
	exit 1
fi
echo "release-test: all scenarios passed"
