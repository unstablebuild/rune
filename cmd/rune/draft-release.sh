#!/bin/bash
# Create the draft GitHub release for the tag at HEAD unless it already exists.
# Shared by dist.sh and the Release workflow so every artifact lands in the
# same draft.
#
# A draft is invisible to users and to the upgrader, which reads
# releases/latest, so artifacts can be tested before the release is published.
# --verify-tag refuses a tag that was not pushed: publishing a draft whose tag
# does not exist would create the tag at the default branch instead.
#
# Optional:
#   RELEASE_REPO  - GitHub repo to create the release in
#                   (default: unstablebuild/rune)
set -e

RELEASE_REPO="${RELEASE_REPO:-unstablebuild/rune}"
source "$(dirname "${BASH_SOURCE[0]}")/check-release-tag.sh"

if gh release view "$GIT_TAG" --repo "$RELEASE_REPO" >/dev/null 2>&1; then
	exit 0
fi
echo "Creating draft release ${GIT_TAG} in ${RELEASE_REPO} ..."
gh release create "$GIT_TAG" --repo "$RELEASE_REPO" --draft --verify-tag \
	--title "$GIT_TAG" --generate-notes
