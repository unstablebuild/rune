ROOT_DIR ?= ../..
GO ?= go
UNAME := $(shell uname)
CODESIGN_IDENTITY ?= Developer ID Application: Unstable Build, LLC. (YYZRWD888J)

BIN := $(abspath $(ROOT_DIR)/bin)
PKG := $(abspath $(ROOT_DIR)/pkg/runectl)
TAR := $(abspath $(ROOT_DIR)/runectl.tar.gz)
ROOT_DIST_SH := ./cmd/runectl/dist.sh
TARGET := $(abspath $(ROOT_DIR)/target)
RUNECTL_PKG := unstable.build/rune/cmd/runectl

.PHONY: clean build pkg sign dist \
	pkg-linux-amd64 pkg-linux-arm64 pkg-darwin-amd64 pkg-darwin-arm64 \
	sign-darwin-amd64 sign-darwin-arm64 \
	dist-linux-amd64 dist-linux-arm64 dist-darwin-amd64 dist-darwin-arm64

build: $(BIN)/runectl

$(BIN)/runectl:
	@mkdir -p $(BIN)
	@GOBIN="$(BIN)" $(GO) install $(RUNECTL_PKG)

clean:
	@rm -rf $(PKG) $(TAR) $(BIN)/runectl $(TARGET)/runectl_*

pkg: $(BIN)/runectl
	@mkdir -p $(PKG)/bin
	@cp $(BIN)/runectl $(PKG)/bin/

ifeq ($(UNAME),Darwin)
sign: pkg
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(PKG)/bin/runectl

$(TAR): sign
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist or runectl-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)
else
sign: pkg
	@echo "Skipping codesign (not on macOS)"

$(TAR): pkg
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist or runectl-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)
endif

# pkg-{os}-{arch} cross-build runectl without cgo and stage it under
# target/runectl_{os}_{arch}/bin for arch-specific packaging and publishing.
define runectl_pkg
	@rm -rf $(TARGET)/runectl_$(1)_$(2)
	@mkdir -p $(TARGET)/runectl_$(1)_$(2)/bin
	@cd $(ROOT_DIR) && CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) \
		$(GO) build -o $(TARGET)/runectl_$(1)_$(2)/bin/runectl $(RUNECTL_PKG)
endef

pkg-linux-amd64:
	$(call runectl_pkg,linux,amd64)

pkg-linux-arm64:
	$(call runectl_pkg,linux,arm64)

pkg-darwin-amd64:
	$(call runectl_pkg,darwin,amd64)

pkg-darwin-arm64:
	$(call runectl_pkg,darwin,arm64)

ifeq ($(UNAME),Darwin)
sign-darwin-amd64: pkg-darwin-amd64
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(TARGET)/runectl_darwin_amd64/bin/runectl

sign-darwin-arm64: pkg-darwin-arm64
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(TARGET)/runectl_darwin_arm64/bin/runectl
else
sign-darwin-amd64: pkg-darwin-amd64
	@echo "Cannot sign Darwin artifacts outside macOS" >&2
	@exit 1

sign-darwin-arm64: pkg-darwin-arm64
	@echo "Cannot sign Darwin artifacts outside macOS" >&2
	@exit 1
endif

# dist-{os}-{arch} publish tarballs stamped with the artifact's target rather
# than the platform of the machine that performed the cross-build.
dist-linux-amd64: pkg-linux-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist-linux-amd64 or runectl-staging-dist-linux-amd64}
	@cd $(TARGET)/runectl_linux_amd64 && tar -czvf $(TARGET)/runectl-linux-amd64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/runectl-linux-amd64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-linux-arm64: pkg-linux-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist-linux-arm64 or runectl-staging-dist-linux-arm64}
	@cd $(TARGET)/runectl_linux_arm64 && tar -czvf $(TARGET)/runectl-linux-arm64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/runectl-linux-arm64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)

dist-darwin-amd64: sign-darwin-amd64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist-darwin-amd64 or runectl-staging-dist-darwin-amd64}
	@cd $(TARGET)/runectl_darwin_amd64 && tar -czvf $(TARGET)/runectl-darwin-amd64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/runectl-darwin-amd64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=amd64 \
		$(ROOT_DIST_SH)

dist-darwin-arm64: sign-darwin-arm64
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use runectl-prod-dist-darwin-arm64 or runectl-staging-dist-darwin-arm64}
	@cd $(TARGET)/runectl_darwin_arm64 && tar -czvf $(TARGET)/runectl-darwin-arm64.tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/runectl-darwin-arm64.tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=arm64 \
		$(ROOT_DIST_SH)
