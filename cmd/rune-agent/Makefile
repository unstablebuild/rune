ROOT_DIR ?= ../..
GO ?= go

BIN := $(abspath $(ROOT_DIR)/bin)
PKG := $(abspath $(ROOT_DIR)/pkg/rune-agent)
TAR := $(abspath $(ROOT_DIR)/rune-agent.tar.gz)
DIST_SH := ./dist.sh
ROOT_DIST_SH := ./cmd/rune-agent/dist.sh

VERSION ?= $(shell git -C $(ROOT_DIR) describe --tags 2>/dev/null)
COMMIT  ?= $(shell git -C $(ROOT_DIR) rev-parse --short HEAD 2>/dev/null)
TARGET := $(abspath $(ROOT_DIR)/target)
RUNE_AGENT_LINUX_TARGET_OS ?= linux
RUNE_AGENT_LINUX_TARGET_ARCH ?= amd64
RUNE_AGENT_LINUX_OUTPUT_DIR ?= $(TARGET)/rune-agent_$(RUNE_AGENT_LINUX_TARGET_OS)_$(RUNE_AGENT_LINUX_TARGET_ARCH)
RUNE_AGENT_LINUX_RELEASE_TAR ?= rune-agent-release-$(RUNE_AGENT_LINUX_TARGET_OS)-$(RUNE_AGENT_LINUX_TARGET_ARCH)-$(VERSION).tar.gz

# Build flags mirror the root Makefile's six package stamp.
COMMON_LDFLAGS := -X unstable.build/rune/internal/debug.Tag=$(VERSION) -X unstable.build/rune/internal/debug.Commit=$(COMMIT) -X unstable.build/rune/internal/debug.Package=six

# Releases build with cgo off, which the root Makefile requires of rune-agent, so every
# os/arch is a plain GOOS/GOARCH build on any host and Linux binaries are static.
# macOS binaries ship without a Developer ID signature or notarization: Rune installs
# them itself, verifying the tarball's PGP signature and clearing quarantine, so
# Gatekeeper never assesses them, and the Go linker ad-hoc signs darwin/arm64 binaries,
# which Apple Silicon requires.
RELEASE_BUILD = cd $(ROOT_DIR) && CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build

.PHONY: clean build pkg dist \
	make-release-linux \
	release-linux-amd64 release-linux-arm64 \
	dist-linux-amd64 dist-linux-arm64 \
	pkg-darwin-amd64 pkg-darwin-arm64 \
	dist-darwin-amd64 dist-darwin-arm64

build: $(BIN)/rune-agent

$(BIN)/rune-agent:
	@$(MAKE) -C $(ROOT_DIR) $(notdir $@)

clean:
	@rm -rf $(PKG) $(TAR) $(BIN)/rune-agent

pkg: $(BIN)/rune-agent
	@mkdir -p $(PKG)/bin
	@cp $(BIN)/rune-agent $(PKG)/bin/
	@cp config.yaml $(PKG)/
	@mkdir -p $(PKG)/skills
	@cp -r skills/code-* $(PKG)/skills/
	@go_files=$$(find $(PKG) -name '*.go'); \
		if [ -n "$$go_files" ]; then \
			echo "error: .go files found in package:" >&2; \
			echo "$$go_files" >&2; \
			exit 1; \
		fi

$(TAR): pkg
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use rune-agent-prod-dist or rune-agent-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)

# make-release-linux builds the rune-agent bundle for RUNE_AGENT_LINUX_TARGET_ARCH:
#   rune-agent/bin/rune-agent
#   rune-agent/config.yaml
#   rune-agent/skills/
# The tarball is rooted at the bundle contents (bin/, config.yaml, skills/), not the
# rune-agent/ wrapper, so config.yaml sits at the archive root where the installer
# looks for it. COPYFILE_DISABLE and --no-xattrs keep a macOS host from adding
# AppleDouble ._ entries and extended attributes to the Linux archive.
make-release-linux:
	@rm -rf $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent
	@mkdir -p $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent/bin
	@$(call RELEASE_BUILD,linux,$(RUNE_AGENT_LINUX_TARGET_ARCH)) \
		-ldflags="$(COMMON_LDFLAGS) -X unstable.build/rune/internal/debug.Package=rune" \
		-o $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent/bin/rune-agent ./cmd/rune-agent
	@cp config.yaml $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent/config.yaml
	@mkdir -p $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent/skills
	@cp -r skills/code-* $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent/skills/
	@cd $(RUNE_AGENT_LINUX_OUTPUT_DIR)/rune-agent && COPYFILE_DISABLE=1 tar --format=ustar \
		--no-xattrs --no-acls -czf $(RUNE_AGENT_LINUX_OUTPUT_DIR)/$(RUNE_AGENT_LINUX_RELEASE_TAR) .

release-linux-amd64:
	@RUNE_AGENT_LINUX_TARGET_ARCH=amd64 \
		RUNE_AGENT_LINUX_RELEASE_TAR=rune-agent-release-linux-amd64-$$(git -C $(ROOT_DIR) describe --tags --dirty).tar.gz \
		$(MAKE) make-release-linux

release-linux-arm64:
	@RUNE_AGENT_LINUX_TARGET_ARCH=arm64 \
		RUNE_AGENT_LINUX_RELEASE_TAR=rune-agent-release-linux-arm64-$$(git -C $(ROOT_DIR) describe --tags --dirty).tar.gz \
		$(MAKE) make-release-linux

dist-linux-amd64: release-linux-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use rune-agent-prod-dist-linux-amd64 or rune-agent-staging-dist-linux-amd64}
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune-agent_linux_amd64/rune-agent-release-linux-amd64-$$(git describe --tags --dirty).tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-linux-arm64: release-linux-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use rune-agent-prod-dist-linux-arm64 or rune-agent-staging-dist-linux-arm64}
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune-agent_linux_arm64/rune-agent-release-linux-arm64-$$(git describe --tags --dirty).tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)

# pkg-darwin-{amd64,arm64} build the rune-agent binary for a specific macOS arch and
# stage config.yaml + skills next to it.
define rune_agent_pkg_darwin
	@rm -rf $(TARGET)/rune-agent_darwin_$(1)
	@mkdir -p $(TARGET)/rune-agent_darwin_$(1)/bin
	@$(call RELEASE_BUILD,darwin,$(1)) -ldflags="$(COMMON_LDFLAGS)" \
		-o $(TARGET)/rune-agent_darwin_$(1)/bin/rune-agent ./cmd/rune-agent
	@cp config.yaml $(TARGET)/rune-agent_darwin_$(1)/
	@mkdir -p $(TARGET)/rune-agent_darwin_$(1)/skills
	@cp -r skills/code-* $(TARGET)/rune-agent_darwin_$(1)/skills/
	@go_files=$$(find $(TARGET)/rune-agent_darwin_$(1) -name '*.go'); \
		if [ -n "$$go_files" ]; then \
			echo "error: .go files found in package:" >&2; \
			echo "$$go_files" >&2; \
			exit 1; \
		fi
endef

pkg-darwin-amd64:
	$(call rune_agent_pkg_darwin,amd64)

pkg-darwin-arm64:
	$(call rune_agent_pkg_darwin,arm64)

dist-darwin-amd64: pkg-darwin-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use rune-agent-prod-dist-darwin-amd64 or rune-agent-staging-dist-darwin-amd64}
	@cd $(TARGET)/rune-agent_darwin_amd64 && tar -czvf $(TARGET)/rune-agent-darwin-amd64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune-agent-darwin-amd64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-darwin-arm64: pkg-darwin-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use rune-agent-prod-dist-darwin-arm64 or rune-agent-staging-dist-darwin-arm64}
	@cd $(TARGET)/rune-agent_darwin_arm64 && tar -czvf $(TARGET)/rune-agent-darwin-arm64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune-agent-darwin-arm64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)
