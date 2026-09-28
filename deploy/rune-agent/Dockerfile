# syntax=docker/dockerfile:1

# rune-agent Linux build (cross-Go, no native libs).
#
# rune-agent does not link llama.cpp; local-model support is provided by
# running the OpenAI-compatible `llama-server` binary as a managed host-side
# subprocess. We still cross-compile through the Go toolchain on
# $BUILDPLATFORM to avoid the qemu-user pointer corruption
# (https://github.com/golang/go/issues/46527).
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

# ---------------------------------------------------------------------------
# build: cross-compile the rune-agent Go binary on $BUILDPLATFORM (no qemu),
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG GIT_SSH_KEY
ARG RUNE_BUILD_TAG
ARG RUNE_BUILD_COMMIT
ARG RUNE_RELEASE_TAR

ENV GOPRIVATE=github.com/unstablebuild,unstable.build/* \
	CGO_ENABLED=1 \
	DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
	if [ "$TARGETOS" != "linux" ]; then \
		echo "unsupported target OS: $TARGETOS (expected linux)"; exit 1; \
	fi; \
	case "$TARGETARCH" in \
		amd64) DPKG_ARCH=amd64; CROSS_ESSENTIAL=crossbuild-essential-amd64 ;; \
		arm64) DPKG_ARCH=arm64; CROSS_ESSENTIAL=crossbuild-essential-arm64 ;; \
		*) echo "unsupported target arch: $TARGETARCH"; exit 1 ;; \
	esac; \
	dpkg --add-architecture "$DPKG_ARCH"; \
	apt-get update; \
	apt-get install -y --no-install-recommends \
		pkg-config bash make git openssh-client ca-certificates file \
		"$CROSS_ESSENTIAL"; \
	rm -rf /var/lib/apt/lists/*

WORKDIR /src

RUN git config --global url.ssh://git@github.com/.insteadOf https://github.com/
RUN mkdir -p ~/.ssh && chmod 700 ~/.ssh && \
	printf '%s\n' "$GIT_SSH_KEY" | tr -d '\r' > ~/.ssh/id_rsa && \
	chmod 600 ~/.ssh/id_rsa
# rune-agent's private deps live on github.com/unstablebuild/*; we do not
# pull from git.unstable.build. Tolerate transient DNS/network failures.
RUN ssh-keyscan -t rsa github.com >> ~/.ssh/known_hosts || true

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
	set -eux; \
	: "${RUNE_BUILD_TAG:?RUNE_BUILD_TAG build arg is required}"; \
	: "${RUNE_BUILD_COMMIT:?RUNE_BUILD_COMMIT build arg is required}"; \
	case "$TARGETARCH" in \
		amd64) \
			CC=x86_64-linux-gnu-gcc; \
			CXX=x86_64-linux-gnu-g++; \
			TRIPLET=x86_64-linux-gnu; \
			OBJDUMP=x86_64-linux-gnu-objdump; \
			TAGS=""; \
			;; \
		arm64) \
			CC=aarch64-linux-gnu-gcc; \
			CXX=aarch64-linux-gnu-g++; \
			TRIPLET=aarch64-linux-gnu; \
			OBJDUMP=aarch64-linux-gnu-objdump; \
			TAGS=""; \
			;; \
	esac; \
	export CC CXX; \
	mkdir -p /out/rune-agent/bin /out/rune-agent/lib; \
	CGO_LDFLAGS="-Wl,-rpath,\$ORIGIN/../lib" \
	GOOS=$TARGETOS \
	GOARCH=$TARGETARCH \
	go build \
		-tags="$TAGS" \
		-ldflags="-X unstable.build/rune/internal/debug.Tag=$RUNE_BUILD_TAG -X unstable.build/rune/internal/debug.Commit=$RUNE_BUILD_COMMIT -X unstable.build/rune/internal/debug.Package=rune" \
		-o /out/rune-agent/bin/rune-agent \
		./cmd/rune-agent; \
	\
	echo "--- artifact validation ---"; \
	file /out/rune-agent/bin/rune-agent | grep 'ELF 64-bit LSB'; \
	"$OBJDUMP" -p /out/rune-agent/bin/rune-agent | awk '$1 == "NEEDED" {print $2}' > /tmp/runtime-needed.txt; \
	cat /tmp/runtime-needed.txt; \
	\
	echo "--- bundling runtime libraries into rune-agent/lib/ (rpath=\$ORIGIN/../lib) ---"; \
	cp /tmp/runtime-needed.txt /tmp/lib-queue.txt; \
	while [ -s /tmp/lib-queue.txt ]; do \
		: > /tmp/lib-next.txt; \
		while IFS= read -r soname; do \
			case "$soname" in \
				libc.so*|libm.so*|libpthread.so*|libdl.so*|librt.so*|ld-linux*|linux-vdso*) continue ;; \
				libcuda.so*) \
					echo "  skipped (host driver): $soname"; \
					continue ;; \
			esac; \
			[ -f "/out/rune-agent/lib/$soname" ] && continue; \
			src=""; \
			for d in /usr/lib/$TRIPLET /lib/$TRIPLET /usr/$TRIPLET/lib; do \
				if [ -e "$d/$soname" ]; then src="$d/$soname"; break; fi; \
			done; \
			if [ -z "$src" ]; then \
				echo "WARN: $soname not found"; continue; \
			fi; \
			cp -L "$src" "/out/rune-agent/lib/$soname"; \
			echo "  bundled: $soname"; \
			"$OBJDUMP" -p "$src" 2>/dev/null | awk '$1 == "NEEDED" {print $2}' >> /tmp/lib-next.txt; \
		done < /tmp/lib-queue.txt; \
		mv /tmp/lib-next.txt /tmp/lib-queue.txt; \
	done; \
	echo "--- bundled libraries ---"; \
	ls -la /out/rune-agent/lib/ || true

RUN set -eux; \
	cp /src/cmd/rune-agent/config.yaml /out/rune-agent/config.yaml; \
	mkdir -p /out/rune-agent/skills; \
	cp -r /src/cmd/rune-agent/skills/code-* /out/rune-agent/skills/

RUN set -eux; \
	: "${RUNE_RELEASE_TAR:?RUNE_RELEASE_TAR build arg is required}"; \
	cd /out/rune-agent; \
	tar --format=ustar --no-xattrs --no-acls -czf "/out/$RUNE_RELEASE_TAR" .; \
	tar -tzf "/out/$RUNE_RELEASE_TAR" >/dev/null

FROM scratch AS artifact
COPY --from=build /out/ /
