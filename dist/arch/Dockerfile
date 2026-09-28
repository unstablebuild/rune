# syntax=docker/dockerfile:1
#
# Builds the Arch package from dist/arch/PKGBUILD so no Arch host is
# needed. Driven by `make pkg-arch` / `make pkg-arch-srcinfo`; see
# dist/README.md.
#
# The build context is the repo root so the package can be built from
# the tree under test. The shipped source= line clones a published tag,
# which is what an AUR user gets, but building that in CI would validate
# a release instead of the commit being reviewed.
#
# archlinux is published for amd64 only, so `make pkg-arch` pins
# --platform linux/amd64 and relies on emulation on other hosts.
FROM archlinux:base-devel AS base

# pacman >= 6.1 sandboxes downloads with seccomp + an unprivileged
# 'alpm' user, which Docker's default seccomp profile rejects
# ("error restricting syscalls via seccomp: 22"). Turn it off in
# pacman.conf rather than per command so makepkg --syncdeps, which
# shells out to pacman, is covered too. Safe here: this is an
# ephemeral build container and packages are still signature-checked.
RUN sed -i '/^\[options\]/a DisableSandbox' /etc/pacman.conf

# sudo is no longer part of base-devel but makepkg -s needs it to let
# pacman install the declared dependencies.
RUN set -eux; \
	pacman -Syu --noconfirm --needed go git namcap sudo; \
	pacman -Scc --noconfirm

# makepkg refuses to run as root.
RUN set -eux; \
	useradd -m builder; \
	printf 'builder ALL=(ALL) NOPASSWD: ALL\n' > /etc/sudoers.d/builder

USER builder
WORKDIR /home/builder/pkg
COPY --chown=builder:builder dist/arch/PKGBUILD .

# .SRCINFO is the metadata file the AUR requires alongside PKGBUILD; it
# must be regenerated from the PKGBUILD on every version bump. This
# stage only parses the PKGBUILD, so it stays fast under emulation.
FROM base AS srcinfo
RUN set -eux; \
	makepkg --printsrcinfo > /home/builder/.SRCINFO; \
	echo "--- .SRCINFO ---"; \
	cat /home/builder/.SRCINFO; \
	echo "--- namcap PKGBUILD ---"; \
	namcap PKGBUILD

FROM scratch AS srcinfo-artifact
COPY --from=srcinfo /home/builder/.SRCINFO /

# Full package build. source= is swapped for the staged tree: a git
# worktree's .git is a file pointing outside the build context, so the
# sources are copied rather than cloned.
FROM base AS build
ARG RUNE_TAG
ARG RUNE_COMMIT
COPY --chown=builder:builder . /src
RUN set -eux; \
	: "${RUNE_TAG:?RUNE_TAG build arg is required}"; \
	pkgver=$(printf '%s' "${RUNE_TAG#v}" | tr '-' '.'); \
	sed -i \
		-e "s|^pkgver=.*|pkgver=$pkgver|" \
		-e 's|^source=.*|source=()|' \
		-e 's|^sha256sums=.*|sha256sums=()|' \
		PKGBUILD; \
	printf '\nprepare() {\n\tcp -a /src "$srcdir/$pkgname"\n}\n' >> PKGBUILD
RUN set -eux; \
	RUNE_COMMIT="$RUNE_COMMIT" \
		makepkg --syncdeps --noconfirm --noprogressbar --nosign

# namcap on the built package catches missing or redundant dependencies
# and bad permissions. Advisory: it also flags things that are correct
# for this package (e.g. the Go binary's static linking).
RUN set -eux; \
	echo "--- namcap package ---"; \
	namcap ./*.pkg.tar.zst || true; \
	echo "--- contents ---"; \
	for pkg in ./*.pkg.tar.zst; do \
		echo "--- $pkg ---"; \
		bsdtar -tf "$pkg"; \
	done

# The GUI libraries are optdepends, so the binary must name nothing but
# glibc in DT_NEEDED or pacman would ship something the dynamic loader
# refuses to start on a host without a graphical stack. External linking
# adds libresolv for the cgo resolver, which glibc provides too.
RUN set -eux; \
	tmp=$(mktemp -d); \
	bsdtar -xf ./rune-*.pkg.tar.zst -C "$tmp" usr/lib/rune.app/bin/rune; \
	unexpected=$(objdump -p "$tmp/usr/lib/rune.app/bin/rune" | \
		awk '$1 == "NEEDED" && $2 !~ /^lib(c|m|dl|rt|pthread|resolv)\.so\./ { print $2 }'); \
	test -z "$unexpected"

RUN set -eux; \
	mkdir -p /home/builder/out; \
	cp ./*.pkg.tar.zst /home/builder/out/

FROM scratch AS artifact
COPY --from=build /home/builder/out/ /
