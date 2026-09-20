# syntax=docker/dockerfile:1

# fuzzy-search extension Linux build (cross-Go, CGO for tree-sitter).
#
# We cross-compile through the Go toolchain on $BUILDPLATFORM to avoid the
# qemu-user pointer corruption (https://github.com/golang/go/issues/46527).
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

# ---------------------------------------------------------------------------
# build: cross-compile the extension_fuzzy_search Go binary on $BUILDPLATFORM.
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
# Tolerate transient DNS/network failures populating known_hosts.
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
			;; \
		arm64) \
			CC=aarch64-linux-gnu-gcc; \
			CXX=aarch64-linux-gnu-g++; \
			TRIPLET=aarch64-linux-gnu; \
			OBJDUMP=aarch64-linux-gnu-objdump; \
			;; \
	esac; \
	export CC CXX; \
	mkdir -p /out/fuzzy-search/bin /out/fuzzy-search/lib; \
	CGO_LDFLAGS="-Wl,-rpath,\$ORIGIN/../lib" \
	GOOS=$TARGETOS \
	GOARCH=$TARGETARCH \
	go build \
		-ldflags="-X unstable.build/rune/internal/debug.Tag=$RUNE_BUILD_TAG -X unstable.build/rune/internal/debug.Commit=$RUNE_BUILD_COMMIT -X unstable.build/rune/internal/debug.Package=six" \
		-o /out/fuzzy-search/bin/extension_fuzzy_search \
		./cmd/extension_fuzzy_search; \
	\
	echo "--- artifact validation ---"; \
	file /out/fuzzy-search/bin/extension_fuzzy_search | grep 'ELF 64-bit LSB'; \
	"$OBJDUMP" -p /out/fuzzy-search/bin/extension_fuzzy_search | awk '$1 == "NEEDED" {print $2}' > /tmp/runtime-needed.txt; \
	cat /tmp/runtime-needed.txt; \
	\
	echo "--- bundling runtime libraries into fuzzy-search/lib/ (rpath=\$ORIGIN/../lib) ---"; \
	cp /tmp/runtime-needed.txt /tmp/lib-queue.txt; \
	while [ -s /tmp/lib-queue.txt ]; do \
		: > /tmp/lib-next.txt; \
		while IFS= read -r soname; do \
			case "$soname" in \
				libc.so*|libm.so*|libpthread.so*|libdl.so*|librt.so*|ld-linux*|linux-vdso*) continue ;; \
			esac; \
			[ -f "/out/fuzzy-search/lib/$soname" ] && continue; \
			src=""; \
			for d in /usr/lib/$TRIPLET /lib/$TRIPLET /usr/$TRIPLET/lib; do \
				if [ -e "$d/$soname" ]; then src="$d/$soname"; break; fi; \
			done; \
			if [ -z "$src" ]; then \
				echo "WARN: $soname not found"; continue; \
			fi; \
			cp -L "$src" "/out/fuzzy-search/lib/$soname"; \
			echo "  bundled: $soname"; \
			"$OBJDUMP" -p "$src" 2>/dev/null | awk '$1 == "NEEDED" {print $2}' >> /tmp/lib-next.txt; \
		done < /tmp/lib-queue.txt; \
		mv /tmp/lib-next.txt /tmp/lib-queue.txt; \
	done; \
	echo "--- bundled libraries ---"; \
	ls -la /out/fuzzy-search/lib/ || true

RUN set -eux; \
	cp /src/cmd/extension_fuzzy_search/config.star /out/fuzzy-search/config.star

RUN set -eux; \
	: "${RUNE_RELEASE_TAR:?RUNE_RELEASE_TAR build arg is required}"; \
	cd /out/fuzzy-search; \
	tar --format=ustar --no-xattrs --no-acls -czf "/out/$RUNE_RELEASE_TAR" .; \
	tar -tzf "/out/$RUNE_RELEASE_TAR" >/dev/null

FROM scratch AS artifact
COPY --from=build /out/ /
