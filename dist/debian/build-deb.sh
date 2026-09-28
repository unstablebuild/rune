#!/usr/bin/env bash
# Build rune_<version>_<arch>.deb from the current checkout.
#
# Stages the working tree into a temp directory, generates
# debian/changelog, and runs dpkg-buildpackage -b. Output lands in
# dist/out/.
#
# Prefer `make pkg-deb`, which runs this in a Debian container. Run it
# directly only on a Debian/Ubuntu host with dpkg-buildpackage,
# debhelper and Go >= 1.26 in PATH (-d skips the distro golang
# build-dep check), plus the dev headers listed in debian/control.
#
# RUNE_TAG / RUNE_COMMIT override the values normally taken from git,
# for builds from a tree without git metadata (e.g. a Docker context
# that excludes .git).
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$here/../.." && pwd)

for tool in dpkg-buildpackage dh go; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "ERROR: $tool not found in PATH" >&2
		exit 1
	fi
done

# debian/rules stamps the version ldflags into internal/debug. A tree
# that predates the internal/ layout would build fine but silently ship
# an unstamped binary (empty Tag/BuildDate), so refuse it outright.
if [ ! -d "$repo_root/internal/debug" ]; then
	echo "ERROR: $repo_root has no internal/debug package." >&2
	echo "       dist/debian targets the internal/ layout on main; check out main." >&2
	exit 1
fi

# --match 'v*' so non-release tags (e.g. pre-apache-backup) cannot
# become the package version; the repo's release policy requires
# canonical semver tags (see cmd/rune/check-release-tag.sh).
tag=${RUNE_TAG:-$(git -C "$repo_root" describe --tags --match 'v*')}
commit=${RUNE_COMMIT:-$(git -C "$repo_root" rev-parse --short HEAD)}
# v1.2.0-beta.1-94-gabc -> 1.2.0~beta.1.94.gabc: the first '-' becomes
# '~' so pre-releases sort before the release; later '-'s become '.'
# because '-' separates the (unused) Debian revision.
deb_version=$(printf '%s' "${tag#v}" | sed -e 's/-/~/' -e 's/-/./g')

# dpkg requires an upstream version starting with a digit; its own
# error for this is cryptic and only surfaces after staging.
case "$deb_version" in
	[0-9]*) ;;
	*)
		echo "ERROR: '$tag' does not yield a valid Debian version ('$deb_version')." >&2
		echo "       Expected a canonical semver tag such as v1.2.3." >&2
		exit 1
		;;
esac

staging=$(mktemp -d "${TMPDIR:-/tmp}/rune-deb-XXXXXX")
trap 'rm -rf "$staging"' EXIT
src="$staging/rune-$deb_version"
mkdir -p "$src"

(cd "$repo_root" && tar -cf - \
	--exclude ./.git \
	--exclude ./bin \
	--exclude ./target \
	--exclude ./debian \
	--exclude ./dist/out \
	.) | tar -xf - -C "$src"

cp -R "$here/debian" "$src/debian"
cat > "$src/debian/changelog" <<EOF
rune ($deb_version) unstable; urgency=medium

  * Upstream build $tag ($commit).

 -- Unstable Build LLC <support@unstable.build>  $(date -u -R)
EOF

(cd "$src" && RUNE_TAG="$tag" RUNE_COMMIT="$commit" \
	dpkg-buildpackage -b -us -uc -d)

out="$repo_root/dist/out"
mkdir -p "$out"
mv "$staging"/rune_*.deb "$out/"
echo "Built:"
ls -1 "$out"/rune_*.deb
