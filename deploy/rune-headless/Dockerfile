# syntax=docker/dockerfile:1
#
# unstablebuild/rune: `rune --headless`, a Rune network node with no
# editor, as a container image. Built from the working tree by
# `make rune-docker-build`; the Headless guide documents how to run it.
#
# Rune is built against musl so the image can be Alpine. The binary
# dlopens its X11, OpenGL and audio libraries only when a window opens,
# so their headers are needed to compile but nothing graphical ships.
#
# The build cross-compiles on $BUILDPLATFORM with xx rather than under
# emulation, because the Go toolchain crashes under qemu-user
# (https://github.com/golang/go/issues/46527).
ARG GO_VERSION=1.27.1
ARG ALPINE_VERSION=3.24
ARG XX_VERSION=1.8.0

FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
COPY --from=xx / /
RUN apk add --no-cache binutils clang git lld

ARG TARGETPLATFORM
RUN xx-apk add --no-cache \
	gcc musl-dev \
	mesa-dev libx11-dev libxrandr-dev libxcursor-dev libxinerama-dev \
	libxi-dev libxxf86vm-dev alsa-lib-dev wayland-dev libxkbcommon-dev

# Computed on the host: the build context excludes .git.
ARG RUNE_BUILD_TAG
ARG RUNE_BUILD_COMMIT
ARG RUNE_BUILD_DATE

WORKDIR /src
COPY . .

# OSPackaged turns the in-product upgrader off: the image owns the
# binary, and a new image is how the node upgrades.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	set -eux; \
	: "${RUNE_BUILD_TAG:?RUNE_BUILD_TAG build arg is required}"; \
	: "${RUNE_BUILD_COMMIT:?RUNE_BUILD_COMMIT build arg is required}"; \
	: "${RUNE_BUILD_DATE:?RUNE_BUILD_DATE build arg is required}"; \
	CGO_ENABLED=1 xx-go build \
		-trimpath \
		-tags=ebitensinglethread \
		-ldflags="-s -w \
			-X unstable.build/rune/internal/debug.Tag=$RUNE_BUILD_TAG \
			-X unstable.build/rune/internal/debug.Commit=$RUNE_BUILD_COMMIT \
			-X unstable.build/rune/internal/debug.BuildDate=$RUNE_BUILD_DATE \
			-X unstable.build/rune/internal/debug.Package=rune \
			-X unstable.build/rune/internal/debug.OSPackaged=true" \
		-o /out/rune \
		./cmd/rune; \
	xx-verify /out/rune

# The runtime image has no graphical stack, so the binary must need
# nothing from the loader but musl itself.
RUN set -eux; \
	readelf -d /out/rune | awk '/NEEDED/ { print $NF }' | tr -d '[]' > /tmp/needed; \
	cat /tmp/needed; \
	test -z "$(grep -v '^libc\.musl-.*\.so\.1$' /tmp/needed)"

FROM alpine:${ALPINE_VERSION}

# bash is the login shell a rune:// terminal opens. tini runs as PID 1
# to reap the terminals and language servers the node leaves behind.
# ~/.rune is created here so a fresh named volume mounted over it is
# owned by rune rather than root.
RUN set -eux; \
	apk add --no-cache bash ca-certificates tini; \
	adduser -D -u 1000 -s /bin/bash rune; \
	install -d -o rune -g rune /home/rune/.rune

COPY --from=build /out/rune /usr/local/bin/rune

USER rune
WORKDIR /home/rune
ENTRYPOINT ["/sbin/tini", "--", "rune"]
CMD ["--headless"]
