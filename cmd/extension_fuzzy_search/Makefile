ROOT_DIR ?= ../..
GO ?= go

BIN := $(abspath $(ROOT_DIR)/bin)
PKG := $(abspath $(ROOT_DIR)/pkg/fuzzy-search)
TAR := $(abspath $(ROOT_DIR)/fuzzy-search.tar.gz)
ROOT_DIST_SH := ./cmd/extension_fuzzy_search/dist.sh

VERSION ?= $(shell git -C $(ROOT_DIR) describe --tags 2>/dev/null)
COMMIT  ?= $(shell git -C $(ROOT_DIR) rev-parse --short HEAD 2>/dev/null)
TARGET := $(abspath $(ROOT_DIR)/target)

FUZZY_LINUX_TARGET_OS ?= linux
FUZZY_LINUX_TARGET_ARCH ?= amd64
FUZZY_LINUX_OUTPUT_DIR ?= $(TARGET)/fuzzy-search_$(FUZZY_LINUX_TARGET_OS)_$(FUZZY_LINUX_TARGET_ARCH)
FUZZY_LINUX_RELEASE_TAR ?= fuzzy-search-release-$(FUZZY_LINUX_TARGET_OS)-$(FUZZY_LINUX_TARGET_ARCH)-$(VERSION).tar.gz

# Build flags mirror the root Makefile's six package stamp.
COMMON_LDFLAGS := -X unstable.build/rune/internal/debug.Tag=$(VERSION) -X unstable.build/rune/internal/debug.Commit=$(COMMIT) -X unstable.build/rune/internal/debug.Package=six

# Releases build with cgo off, which the root Makefile requires of language extensions,
# so every os/arch is a plain GOOS/GOARCH build on any host and Linux binaries are
# static. macOS binaries ship without a Developer ID signature or notarization: Rune
# installs them itself, verifying the tarball's PGP signature and clearing quarantine,
# so Gatekeeper never assesses them, and the Go linker ad-hoc signs darwin/arm64
# binaries, which Apple Silicon requires.
RELEASE_BUILD = cd $(ROOT_DIR) && CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build

.PHONY: clean build pkg dist \
	make-release-linux \
	release-linux-amd64 release-linux-arm64 \
	dist-linux-amd64 dist-linux-arm64 \
	pkg-darwin-amd64 pkg-darwin-arm64 \
	dist-darwin-amd64 dist-darwin-arm64

build: $(BIN)/extension_fuzzy_search

$(BIN)/extension_fuzzy_search:
	@$(MAKE) -C $(ROOT_DIR) bin/$(notdir $@)

clean:
	@rm -rf $(PKG) $(TAR) $(BIN)/extension_fuzzy_search $(TARGET)/fuzzy-search_*

pkg: $(BIN)/extension_fuzzy_search
	@mkdir -p $(PKG)/bin
	@cp $(BIN)/extension_fuzzy_search $(PKG)/bin/
	@cp config.star $(PKG)/
	@cp fuzzy_search.star $(PKG)/
	@go_files=$$(find $(PKG) -name '*.go'); \
		if [ -n "$$go_files" ]; then \
			echo "error: .go files found in package:" >&2; \
			echo "$$go_files" >&2; \
			exit 1; \
		fi
$(TAR): pkg
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use fuzzy-search-prod-dist or fuzzy-search-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)

# make-release-linux builds the fuzzy-search bundle for FUZZY_LINUX_TARGET_ARCH:
#   fuzzy-search/bin/extension_fuzzy_search
#   fuzzy-search/config.star
#   fuzzy-search/fuzzy_search.star
# The tarball is rooted at the bundle contents (bin/, config.star), not the
# fuzzy-search/ wrapper, so config.star sits at the archive root where the installer
# looks for it. COPYFILE_DISABLE and --no-xattrs keep a macOS host from adding
# AppleDouble ._ entries and extended attributes to the Linux archive.
make-release-linux:
	@rm -rf $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search
	@mkdir -p $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search/bin
	@$(call RELEASE_BUILD,linux,$(FUZZY_LINUX_TARGET_ARCH)) -ldflags="$(COMMON_LDFLAGS)" \
		-o $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search/bin/extension_fuzzy_search ./cmd/extension_fuzzy_search
	@cp config.star $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search/config.star
	@cp fuzzy_search.star $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search/fuzzy_search.star
	@cd $(FUZZY_LINUX_OUTPUT_DIR)/fuzzy-search && COPYFILE_DISABLE=1 tar --format=ustar \
		--no-xattrs --no-acls -czf $(FUZZY_LINUX_OUTPUT_DIR)/$(FUZZY_LINUX_RELEASE_TAR) .

release-linux-amd64:
	@FUZZY_LINUX_TARGET_ARCH=amd64 \
		FUZZY_LINUX_RELEASE_TAR=fuzzy-search-release-linux-amd64-$$(git -C $(ROOT_DIR) describe --tags --dirty).tar.gz \
		$(MAKE) make-release-linux

release-linux-arm64:
	@FUZZY_LINUX_TARGET_ARCH=arm64 \
		FUZZY_LINUX_RELEASE_TAR=fuzzy-search-release-linux-arm64-$$(git -C $(ROOT_DIR) describe --tags --dirty).tar.gz \
		$(MAKE) make-release-linux

dist-linux-amd64: release-linux-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use fuzzy-search-prod-dist-linux-amd64 or fuzzy-search-staging-dist-linux-amd64}
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/fuzzy-search_linux_amd64/fuzzy-search-release-linux-amd64-$$(git describe --tags --dirty).tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-linux-arm64: release-linux-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use fuzzy-search-prod-dist-linux-arm64 or fuzzy-search-staging-dist-linux-arm64}
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/fuzzy-search_linux_arm64/fuzzy-search-release-linux-arm64-$$(git describe --tags --dirty).tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)

# pkg-darwin-{amd64,arm64} build the extension binary for a specific macOS arch and
# stage config.star next to it.
define fuzzy_pkg_darwin
	@rm -rf $(TARGET)/fuzzy-search_darwin_$(1)
	@mkdir -p $(TARGET)/fuzzy-search_darwin_$(1)/bin
	@$(call RELEASE_BUILD,darwin,$(1)) -ldflags="$(COMMON_LDFLAGS)" \
		-o $(TARGET)/fuzzy-search_darwin_$(1)/bin/extension_fuzzy_search ./cmd/extension_fuzzy_search
	@cp config.star $(TARGET)/fuzzy-search_darwin_$(1)/
	@cp fuzzy_search.star $(TARGET)/fuzzy-search_darwin_$(1)/
	@go_files=$$(find $(TARGET)/fuzzy-search_darwin_$(1) -name '*.go'); \
		if [ -n "$$go_files" ]; then \
			echo "error: .go files found in package:" >&2; \
			echo "$$go_files" >&2; \
			exit 1; \
		fi
endef

pkg-darwin-amd64:
	$(call fuzzy_pkg_darwin,amd64)

pkg-darwin-arm64:
	$(call fuzzy_pkg_darwin,arm64)

dist-darwin-amd64: pkg-darwin-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use fuzzy-search-prod-dist-darwin-amd64 or fuzzy-search-staging-dist-darwin-amd64}
	@cd $(TARGET)/fuzzy-search_darwin_amd64 && tar -czvf $(TARGET)/fuzzy-search-darwin-amd64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/fuzzy-search-darwin-amd64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-darwin-arm64: pkg-darwin-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use fuzzy-search-prod-dist-darwin-arm64 or fuzzy-search-staging-dist-darwin-arm64}
	@cd $(TARGET)/fuzzy-search_darwin_arm64 && tar -czvf $(TARGET)/fuzzy-search-darwin-arm64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/fuzzy-search-darwin-arm64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)
