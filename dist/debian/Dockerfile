# syntax=docker/dockerfile:1
#
# Builds rune_<version>_<arch>.deb from the working tree so no Debian
# host is needed. Driven by `make pkg-deb`; see dist/README.md.
#
# Built natively for the target arch (under emulation when the host
# differs) rather than cross-compiled: dpkg-shlibdeps has to inspect
# target-arch ELF objects against target-arch libraries to compute
# ${shlibs:Depends}.
#
# BASE_IMAGE sets the glibc floor of the resulting binary, since the
# .deb links the distro's own shared libraries. bullseye (glibc 2.31)
# is the oldest base that still ships debhelper 13, which covers
# Debian 11+ and Ubuntu 20.04+.
#
# bookworm (glibc 2.36) is the default: it is supported, needs no
# archive workarounds, and covers Debian 12+ / Ubuntu 24.04+. Override
# for a lower floor, e.g. DEB_BASE_IMAGE=debian:bullseye for glibc 2.31
# (Ubuntu 22.04), accepting that the base is EOL.
ARG BASE_IMAGE=debian:bookworm
FROM ${BASE_IMAGE} AS build

# Keep in sync with go.mod's go directive. bullseye's golang-go is far
# too old, so the toolchain comes from the official tarball; it links
# only a low glibc and runs fine here.
ARG GO_VERSION=1.26.6
ARG TARGETARCH
# Computed on the host, where git metadata is available: this build
# context excludes .git (a git worktree's .git is a file pointing
# outside the context anyway), so `git describe` cannot run in here.
ARG RUNE_TAG
ARG RUNE_COMMIT

ENV DEBIAN_FRONTEND=noninteractive

# Check-Valid-Until=false keeps an overridden EOL base (whose Release
# files have expired) from hard-failing apt-get update.
RUN set -eux; \
	apt-get -o Acquire::Check-Valid-Until=false update; \
	apt-get install -y --no-install-recommends \
		build-essential debhelper devscripts fakeroot lintian \
		dpkg-dev ca-certificates curl git pkg-config \
		libgl1-mesa-dev libx11-dev libxrandr-dev libxcursor-dev \
		libxinerama-dev libxi-dev libxxf86vm-dev \
		libasound2-dev libwayland-dev libxkbcommon-dev; \
	rm -rf /var/lib/apt/lists/*

RUN set -eux; \
	curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
		-o /tmp/go.tar.gz; \
	tar -C /usr/local -xzf /tmp/go.tar.gz; \
	rm /tmp/go.tar.gz
ENV PATH=/usr/local/go/bin:$PATH

WORKDIR /src
COPY . .

# build-deb.sh stages the tree into a temp dir, generates
# debian/changelog, and runs dpkg-buildpackage.
RUN set -eux; \
	: "${RUNE_TAG:?RUNE_TAG build arg is required}"; \
	RUNE_TAG="$RUNE_TAG" RUNE_COMMIT="$RUNE_COMMIT" \
		./dist/debian/build-deb.sh

# lintian is advisory here: a fresh package legitimately trips tags
# that only matter for an actual Debian upload (no man page, no
# changelog history). Print them so they can be worked through before
# an ITP without failing the artifact build.
RUN set -eux; \
	cd /src/dist/out; \
	echo "--- lintian ---"; \
	lintian --no-tag-display-limit ./*.deb || true; \
	echo "--- contents ---"; \
	dpkg-deb -c ./*.deb; \
	echo "--- metadata ---"; \
	dpkg-deb -I ./*.deb; \
	tmp=$(mktemp -d); \
	dpkg-deb -x ./*.deb "$tmp"; \
	unexpected=$(objdump -p "$tmp/usr/lib/rune.app/bin/rune" | \
		awk '$1 == "NEEDED" && $2 !~ /^lib(c|m|dl|rt|pthread|resolv)\.so\./ { print $2 }'); \
	test -z "$unexpected"

# Installing without Recommends verifies the actual packaging contract:
# the dynamic loader has to start the binary on a host that has none of
# the X11/OpenGL stack. --version is the deepest entry point that stays
# hermetic; --headless authenticates against the network, which a build
# sandbox cannot satisfy.
FROM ${BASE_IMAGE} AS headless-smoke
COPY --from=build /src/dist/out/ /pkg/
RUN set -eux; \
	dpkg --install /pkg/*.deb; \
	rune --version

FROM scratch AS artifact
COPY --from=headless-smoke /pkg/ /
