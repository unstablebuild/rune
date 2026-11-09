ROOT_DIR ?= ../..
GO ?= go
UNAME := $(shell uname)
CODESIGN_IDENTITY ?= Developer ID Application: Unstable Build, LLC. (YYZRWD888J)
NOTARY_PROFILE ?= notary-profile

PKG := $(abspath $(ROOT_DIR)/pkg/xsandbox)
TAR := $(abspath $(ROOT_DIR)/xsandbox.tar.gz)
NOTARIZE_ZIP := $(abspath $(ROOT_DIR)/xsandbox-notarize.zip)
ROOT_DIST_SH := ./cmd/xsandbox/dist.sh

TARGET := $(abspath $(ROOT_DIR)/target)
# xsandbox links no C libraries, so the whole os/arch matrix is a plain
# GOOS/GOARCH go build and one binary runs on every glibc and macOS release.
XSANDBOX_PKG := ./cmd/xsandbox

.PHONY: clean pkg sign notarize dist dist-notarized \
	pkg-linux-amd64 pkg-linux-arm64 pkg-darwin-amd64 pkg-darwin-arm64 \
	sign-darwin-amd64 sign-darwin-arm64 \
	notarize-darwin-amd64 notarize-darwin-arm64 \
	dist-linux-amd64 dist-linux-arm64 dist-darwin-amd64 dist-darwin-arm64

clean:
	@rm -rf $(PKG) $(TAR) $(NOTARIZE_ZIP) $(TARGET)/xsandbox_* $(TARGET)/xsandbox-*

# The package is built here rather than copied from bin/xsandbox, which the
# root Makefile builds with the developer's cgo setting.
pkg:
	@rm -rf $(PKG)
	@mkdir -p $(PKG)/bin
	@cd $(ROOT_DIR) && CGO_ENABLED=0 $(GO) build -o $(PKG)/bin/xsandbox $(XSANDBOX_PKG)

ifeq ($(UNAME),Darwin)
sign: pkg
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(PKG)/bin/xsandbox

$(NOTARIZE_ZIP): sign
	zip $(NOTARIZE_ZIP) $(PKG)/bin/xsandbox

notarize: $(NOTARIZE_ZIP)
	xcrun notarytool submit $(NOTARIZE_ZIP) --keychain-profile "$(NOTARY_PROFILE)"

$(TAR): sign
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use xsandbox-prod-dist or xsandbox-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)

dist-notarized: clean notarize $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use xsandbox-prod-dist-notarized or xsandbox-staging-dist-notarized}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)
else
sign: pkg
	@echo "Skipping codesign (not on macOS)"

notarize: sign
	@echo "Skipping notarization (not on macOS)"

$(TAR): pkg
	cd $(PKG) && tar -czvf $(TAR) .

dist: clean $(TAR)
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use xsandbox-prod-dist or xsandbox-staging-dist}
	@cd $(ROOT_DIR) && BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR $(ROOT_DIST_SH)

dist-notarized: dist
	@echo "Skipping notarized dist (not on macOS)"
endif

# pkg-{os}-{arch} cross-build the xsandbox binary for a specific target and
# stage it under target/xsandbox_{os}_{arch}/bin.
define xsandbox_pkg
	@rm -rf $(TARGET)/xsandbox_$(1)_$(2)
	@mkdir -p $(TARGET)/xsandbox_$(1)_$(2)/bin
	@cd $(ROOT_DIR) && CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) \
		$(GO) build -o $(TARGET)/xsandbox_$(1)_$(2)/bin/xsandbox $(XSANDBOX_PKG)
endef

# xsandbox_dist tars the staged bundle and uploads it stamped with the
# artifact's real target os/arch (via BLUE_TARGET_OS / BLUE_TARGET_ARCH).
define xsandbox_dist
	@: $${BLUECTL_CONFIG_DIR:?BLUECTL_CONFIG_DIR not set; use xsandbox-prod-dist-$(1)-$(2) or xsandbox-staging-dist-$(1)-$(2)}
	@cd $(TARGET)/xsandbox_$(1)_$(2) && tar -czvf $(TARGET)/xsandbox-$(1)-$(2).tar.gz .
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/xsandbox-$(1)-$(2).tar.gz \
		BLUECTL_CONFIG_DIR=$$BLUECTL_CONFIG_DIR \
		BLUE_TARGET_OS=$(1) \
		BLUE_TARGET_ARCH=$(2) \
		$(ROOT_DIST_SH)
endef

pkg-linux-amd64:
	$(call xsandbox_pkg,linux,amd64)

pkg-linux-arm64:
	$(call xsandbox_pkg,linux,arm64)

pkg-darwin-amd64:
	$(call xsandbox_pkg,darwin,amd64)

pkg-darwin-arm64:
	$(call xsandbox_pkg,darwin,arm64)

ifeq ($(UNAME),Darwin)
sign-darwin-amd64: pkg-darwin-amd64
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(TARGET)/xsandbox_darwin_amd64/bin/xsandbox

sign-darwin-arm64: pkg-darwin-arm64
	codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(TARGET)/xsandbox_darwin_arm64/bin/xsandbox

notarize-darwin-amd64: sign-darwin-amd64
	zip $(TARGET)/xsandbox-notarize-darwin-amd64.zip $(TARGET)/xsandbox_darwin_amd64/bin/xsandbox
	xcrun notarytool submit $(TARGET)/xsandbox-notarize-darwin-amd64.zip --keychain-profile "$(NOTARY_PROFILE)"

notarize-darwin-arm64: sign-darwin-arm64
	zip $(TARGET)/xsandbox-notarize-darwin-arm64.zip $(TARGET)/xsandbox_darwin_arm64/bin/xsandbox
	xcrun notarytool submit $(TARGET)/xsandbox-notarize-darwin-arm64.zip --keychain-profile "$(NOTARY_PROFILE)"
else
sign-darwin-amd64: pkg-darwin-amd64
	@echo "Skipping codesign (not on macOS)"

sign-darwin-arm64: pkg-darwin-arm64
	@echo "Skipping codesign (not on macOS)"

notarize-darwin-amd64: sign-darwin-amd64
	@echo "Skipping notarization (not on macOS)"

notarize-darwin-arm64: sign-darwin-arm64
	@echo "Skipping notarization (not on macOS)"
endif

# darwin targets notarize first so published macOS binaries are signed.
dist-linux-amd64: pkg-linux-amd64
	$(call xsandbox_dist,linux,amd64)

dist-linux-arm64: pkg-linux-arm64
	$(call xsandbox_dist,linux,arm64)

dist-darwin-amd64: notarize-darwin-amd64
	$(call xsandbox_dist,darwin,amd64)

dist-darwin-arm64: notarize-darwin-arm64
	$(call xsandbox_dist,darwin,arm64)