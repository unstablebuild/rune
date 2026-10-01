#!/usr/bin/env bash
# Report what a Rune release still lacks, and publish it to Homebrew.
#
#   release.sh status            list the release's platform artifacts and whether
#                                each distribution channel carries the release
#   release.sh publish-homebrew  publish the cask to the Homebrew tap; fails unless
#                                the release is published and carries every
#                                artifact the cask pins
#
#   RELEASE_TAG   required, e.g. v1.3.0
#   RELEASE_REPO  defaults to unstablebuild/rune
#   TAP_REPO      defaults to unstablebuild/homebrew-rune
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
command=${1:-}
case "$command" in
status | publish-homebrew) ;;
*)
	echo "usage: RELEASE_TAG=vX.Y.Z $0 status|publish-homebrew" >&2
	exit 2
	;;
esac

tag="${RELEASE_TAG:?RELEASE_TAG is required, e.g. v1.3.0}"
release_repo="${RELEASE_REPO:-unstablebuild/rune}"
tap_repo="${TAP_REPO:-unstablebuild/homebrew-rune}"
cask_path=Casks/rune.rb
version=${tag#v}
yes=✅ no=❌ skip=➖

release=$(gh release view "$tag" --repo "$release_repo" --json isDraft,url,assets \
	--jq '.isDraft, .url, .assets[].name')
{
	read -r is_draft
	read -r release_url
	assets=$(cat)
} <<<"$release"

has_asset() {
	printf '%s\n' "$assets" | grep -qx "$@"
}

missing_rows=""
present_rows=""
# row <emoji> <repo> <os>/<arch> <extension>
row() {
	if [ "$1" = "$no" ]; then
		missing_rows+="$*"$'\n'
	else
		present_rows+="$*"$'\n'
	fi
}

check_artifacts() {
	local platform os arch ext artifact
	missing_linux=0 missing_macos=0
	for platform in linux/arm64 linux/amd64 darwin/amd64 darwin/arm64; do
		os=${platform%/*} arch=${platform#*/}
		case "$os" in
		darwin) ext=.dmg artifact="Rune-${tag}-darwin-${arch}.dmg" ;;
		linux) ext=.tar.gz artifact="rune-${tag}-linux-${arch}.tar.gz" ;;
		esac
		if has_asset -F "$artifact" && has_asset -F "manifest-${os}-${arch}.json"; then
			row "$yes" "GitHub release" "$platform" "$ext"
		else
			row "$no" "GitHub release" "$platform" "$ext"
			case "$os" in
			darwin) missing_macos=$((missing_macos + 1)) ;;
			linux) missing_linux=$((missing_linux + 1)) ;;
			esac
		fi
	done
	missing_artifacts=$((missing_linux + missing_macos))
}

check_debian() {
	local arch
	missing_debs=0
	for arch in arm64 amd64; do
		if has_asset "rune_.*_${arch}\.deb"; then
			row "$yes" "GitHub release" "linux/$arch" .deb
		else
			row "$no" "GitHub release" "linux/$arch" .deb
			missing_debs=$((missing_debs + 1))
		fi
	done
}

homebrew_state() {
	case "$tag" in
	*-*)
		homebrew=prerelease
		return
		;;
	esac
	if [ "$missing_artifacts" -gt 0 ]; then
		homebrew=needs-artifacts
		return
	fi
	# Draft assets are not publicly downloadable, so neither the cask's URLs
	# nor the manifests update-cask.sh reads exist yet.
	if [ "$is_draft" = true ]; then
		homebrew=draft
		return
	fi
	cask=$(RELEASE_TAG="$tag" RELEASE_REPO="$release_repo" "$here/homebrew/update-cask.sh")
	tap_cask=$(gh api "repos/$tap_repo/contents/$cask_path" \
		-H 'Accept: application/vnd.github.raw' 2>/dev/null || true)
	tap_version=$(printf '%s\n' "$tap_cask" | awk -F'"' '/^  version /{print $2; exit}')
	if [ "$cask" = "$tap_cask" ]; then
		homebrew=published
	elif [ "$tap_version" = "$version" ]; then
		homebrew=outdated
	elif [ -n "$tap_version" ] &&
		[ "$(printf '%s\n' "$tap_version" "$version" | sort -V | tail -1)" = "$tap_version" ]; then
		homebrew=superseded
	else
		homebrew=missing
	fi
}

# The cask pins every platform, so Homebrew gets one row for all of them.
check_homebrew() {
	homebrew_state
	case "$homebrew" in
	published) row "$yes" Homebrew "*/*" .rb ;;
	prerelease | superseded) row "$skip" Homebrew "*/*" .rb ;;
	*) row "$no" Homebrew "*/*" .rb ;;
	esac
}

report_homebrew() {
	case "$homebrew" in
	prerelease) echo "Homebrew release ($tag) is not applicable: Homebrew only ships stable releases." ;;
	needs-artifacts) echo "Homebrew release ($tag) is missing but requires artifacts." ;;
	draft) echo "Homebrew release ($tag) is missing but requires the release to be published." ;;
	missing) echo "Homebrew release ($tag) is missing AND READY TO BE PUBLISHED.$1" ;;
	outdated) echo "Homebrew release ($tag) is outdated AND READY TO BE PUBLISHED.$1" ;;
	published) echo "Homebrew release ($tag) is published." ;;
	superseded) echo "Homebrew release ($tag) is superseded: $tap_repo already ships v$tap_version." ;;
	esac
}

publish_homebrew() {
	local sha url
	local args=(-X PUT "repos/$tap_repo/contents/$cask_path"
		-f "message=rune $version"
		-f "content=$(printf '%s\n' "$cask" | base64 | tr -d '\n')")
	sha=$(gh api "repos/$tap_repo/contents/$cask_path" --jq .sha 2>/dev/null || true)
	if [ -n "$sha" ]; then
		args+=(-f "sha=$sha")
	fi
	echo "Publishing Homebrew release ($tag) to $tap_repo ..."
	url=$(gh api "${args[@]}" --jq .commit.html_url)
	echo "Homebrew release ($tag) is published: $url"
}

check_artifacts
check_debian
check_homebrew

echo
if [ "$is_draft" = true ]; then
	echo "Release $tag ($release_repo, draft):"
else
	echo "Release $tag ($release_repo):"
fi
printf '%s' "$missing_rows" "$present_rows"
echo
if [ $((missing_linux + missing_debs)) -gt 0 ]; then
	echo "Linux artifacts come from the Release workflow, which runs when the tag is pushed:" \
		"https://github.com/$release_repo/actions/workflows/release.yml"
fi
if [ "$missing_macos" -gt 0 ]; then
	echo "macOS artifacts are signed and attached from a Mac: make rune-prod-dist-darwin-<arch>"
fi
if [ "$is_draft" = true ] && [ $((missing_artifacts + missing_debs)) -eq 0 ]; then
	echo "Release $tag is a draft AND READY TO BE TESTED. Publish it once tested at $release_url"
fi

if [ "$command" = status ]; then
	report_homebrew $' Publish now with\n\nmake rune-prod-dist-homebrew'
	exit 0
fi

report_homebrew ""
case "$homebrew" in
missing | outdated) publish_homebrew ;;
published) ;;
*)
	echo "Nothing was published." >&2
	exit 1
	;;
esac
