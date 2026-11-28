#!/bin/bash
set -e

GIT_REMOTE_URL=$(git remote get-url origin)
GIT_AUTHOR_EMAIL=$(git log -1 --pretty=format:'%ae')
GIT_TAG=$(git describe --tags --dirty)
GIT_HEAD=$(git rev-parse HEAD)
: "${RUNECTL_VERSION:?Set RUNECTL_VERSION to the runectl package version (for example, v1.1.3).}"
: "${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR is not set. Use the runectl-{prod,staging}-dist* make targets so the Bluectl environment and target platform are selected by the target.}"
BLUE_RELEASE_TAR="${BLUE_RELEASE_TAR:-runectl.tar.gz}"
BLUE_EXEC=(bluectl -c "$BLUECTL_CONFIG_DIR")

# Cross-platform targets set these values so the release manifest identifies
# the staged artifact rather than the machine running the publisher.
OS="${BLUE_TARGET_OS:-$(uname | awk '{print tolower($0)}')}"
if [[ -n "$BLUE_TARGET_ARCH" ]]; then
	ARCH="$BLUE_TARGET_ARCH"
elif [[ "$(uname)" == "Darwin" ]] && [[ "$(sysctl -n hw.optional.arm64 2>/dev/null)" == "1" ]]; then
	ARCH="arm64"
else
	ARCH=$(uname -m)
fi
BLUE_RELEASE_TAG="$RUNECTL_VERSION"

echo "Pushing tarball for OS '$OS' and arch '$ARCH'"

if [[ -z "${BLUE_PGP_KEY}" ]]; then
	echo "BLUE_PGP_KEY is not set. See bluectl release upload -h for help."
	exit 1
fi

if [[ -z "${BLUE_PGP_KEYRING}" ]]; then
	echo "BLUE_PGP_KEYRING is not set. See bluectl release upload -h for help."
	exit 1
fi

# Final pre-publish gate: a distribution archive must not carry Go source.
"$(dirname "${BASH_SOURCE[0]}")/../verify-no-go-source.sh" "$BLUE_RELEASE_TAR"

echo "uploading $BLUE_RELEASE_TAG"
"${BLUE_EXEC[@]}" release upload \
	-d target-os="$OS" \
	-d target-arch="$ARCH" \
	-d git-remote-url="$GIT_REMOTE_URL" \
	-d git-author-email="$GIT_AUTHOR_EMAIL" \
	-d git-tag="$GIT_TAG" \
	-d git-head="$GIT_HEAD" \
	-y \
	-k "$BLUE_PGP_KEY" \
	-r "$BLUE_PGP_KEYRING" \
	runectl "$BLUE_RELEASE_TAG" "$BLUE_RELEASE_TAR"
