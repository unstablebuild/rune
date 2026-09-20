ROOT_DIR ?= $(abspath ../..)
GO ?= go
UNAME := $(shell uname)
VERSION=$(shell git describe --tags)
COMMIT=$(shell git rev-parse --short HEAD)
CODESIGN_IDENTITY ?= Developer ID Application: Unstable Build, LLC. (YYZRWD888J)
NOTARY_PROFILE ?= notary-profile
RUNE_LINUX_TARGET_OS ?= linux
RUNE_LINUX_TARGET_ARCH ?= amd64
RUNE_LINUX_OUTPUT_DIR ?= $(TARGET)/rune_$(RUNE_LINUX_TARGET_OS)_$(RUNE_LINUX_TARGET_ARCH)
RUNE_LINUX_RELEASE_TAR ?= rune-release-$(RUNE_LINUX_TARGET_OS)-$(RUNE_LINUX_TARGET_ARCH)-$(VERSION).tar.gz
# HOST_ARCH is the host's Go arch. A native Linux release (the *-native
# targets) requires the host's arch to match the requested target arch (no
# emulation); other combinations must use the default Docker cross-compile
# release targets.
HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
# DARWIN_AMD64_MIN_MACOS is the minimum macOS the Intel build supports. It
# is threaded into the cgo toolchain (-mmacosx-version-min) and the app
# bundle's LSMinimumSystemVersion (extra/osx/Info.amd64.plist) so the Intel
# build runs on the advertised floor.
DARWIN_AMD64_MIN_MACOS ?= 13.3
DARWIN_AMD64_TARGET_ARCH_FLAGS ?= CC="clang -arch x86_64 -mmacosx-version-min=$(DARWIN_AMD64_MIN_MACOS)" CGO_CFLAGS="-arch x86_64 -mmacosx-version-min=$(DARWIN_AMD64_MIN_MACOS)" CGO_CXXFLAGS="-arch x86_64 -mmacosx-version-min=$(DARWIN_AMD64_MIN_MACOS)" CGO_LDFLAGS="-arch x86_64 -mmacosx-version-min=$(DARWIN_AMD64_MIN_MACOS)"

# DARWIN_ARM64_MIN_MACOS is the minimum macOS the Apple-silicon build
# supports. Without an explicit deployment target the cgo toolchain
# records the build host's SDK as the floor, which made arm64 DMGs
# refuse to launch on anything older than the build machine. Kept in
# sync with the arm64 app bundle's LSMinimumSystemVersion
# (extra/osx/Rune.app/Contents/Info.plist).
DARWIN_ARM64_MIN_MACOS ?= 13.3
DARWIN_ARM64_TARGET_ARCH_FLAGS ?= CC="clang -arch arm64 -mmacosx-version-min=$(DARWIN_ARM64_MIN_MACOS)" CGO_CFLAGS="-arch arm64 -mmacosx-version-min=$(DARWIN_ARM64_MIN_MACOS)" CGO_CXXFLAGS="-arch arm64 -mmacosx-version-min=$(DARWIN_ARM64_MIN_MACOS)" CGO_LDFLAGS="-arch arm64 -mmacosx-version-min=$(DARWIN_ARM64_MIN_MACOS)"

# RUNE_MIN_GLIBC is the highest versioned glibc symbol a published Linux
# artifact may require from the host. It is the Linux analogue of
# DARWIN_*_MIN_MACOS: the Docker cross build image (debian:buster-slim)
# guarantees glibc 2.28, so any shipped ELF that needs a newer version
# would fail to load on a floor host ("version `GLIBC_2.29' not found").
# The dist-linux-* rules thread this into cmd/rune/dist.sh, which runs
# verify-min-linux.sh as a fail-closed publish gate. Lower this only if
# the build toolchain's floor is also lowered.
RUNE_MIN_GLIBC   ?= 2.28

# RUNE_DEBUG_BUILD mirrors the root Makefile flag. Set to "true" to
# bake debug-only ex commands (:panic, :crash) into the binary. The
# root `debug` target propagates this; release builds leave it empty.
RUNE_DEBUG_BUILD ?=
DEBUG_LDFLAGS=$(if $(filter true,$(RUNE_DEBUG_BUILD)),-X unstable.build/rune/internal/debug.DebugBuild=true)
# RACE_FLAG mirrors RUNE_DEBUG_BUILD so direct cmd/rune builds (which
# do not run through the root Makefile's GOFLAGS) still get -race when
# the caller opts into a debug build.
RACE_FLAG=$(if $(filter true,$(RUNE_DEBUG_BUILD)),-race)
# BUILD_DATE mirrors the root Makefile: cmd/buildstamp renders the build
# time as RFC3339 UTC (Go, not date(1), formats it so it is portable),
# resolved once at parse time from ROOT_DIR so go run finds the module
# root. The `out=$(...) && printf` guard emits the stamp only when
# buildstamp exits zero, so any failure leaves BUILD_DATE empty (the
# error still reaches stderr). BUILD_DATE_LDFLAG $(error)s on an empty
# stamp so a broken buildstamp aborts the build instead of baking in a
# missing debug.BuildDate that would leave `rune --version` reporting no
# build date.
BUILD_DATE := $(shell out=$$(cd $(ROOT_DIR) && $(GO) run ./cmd/buildstamp) && printf '%s' "$$out")
BUILD_DATE_LDFLAG = $(if $(strip $(BUILD_DATE)),,$(error buildstamp produced no build date; refusing to build a binary with an empty debug.BuildDate))-X unstable.build/rune/internal/debug.BuildDate=$(strip $(BUILD_DATE))
# dist/arch/PKGBUILD and dist/debian/debian/rules re-declare this set
# because makepkg and dpkg-buildpackage drive the compiler themselves and
# never call these rules. A flag added or renamed here has to be mirrored
# in both, or packaged builds quietly ship without it.
COMMON_LDFLAGS=-X unstable.build/rune/internal/debug.Tag=$$(git describe --tags) -X unstable.build/rune/internal/debug.Commit=$$(git rev-parse --short HEAD) $(BUILD_DATE_LDFLAG) $(DEBUG_LDFLAGS)
GOFLAGS=$(RACE_FLAG) -ldflags="$(COMMON_LDFLAGS) -X unstable.build/rune/internal/debug.Package=six"

# RUNE_ENV selects the API endpoints baked into the binary.
#   - prod (default): point at api.rune.build / rpc.rune.build
#   - staging       : point at api.unstable.build / rpc.unstable.build
# Production is the source-level default in cmd/rune/ide/apiclient, so a
# plain `go build` / `go run ./cmd/rune` matches a release build and needs
# no ldflags here. Staging is opt-in: the staging-dist-* rules below set
# RUNE_ENV=staging, which injects STAGING_LDFLAGS.
RUNE_ENV ?= prod
STAGING_LDFLAGS=-X unstable.build/rune/cmd/rune/ide/apiclient.DefaultHTTPEndpointAddress=https://api.unstable.build -X unstable.build/rune/cmd/rune/ide/apiclient.DefaultGRPCEndpointAddress=rpc.unstable.build:443 -X unstable.build/rune/cmd/rune/ide/apiclient.DefaultDownloadsHost=$(STAGING_DOWNLOAD_HOST) -X unstable.build/rune/cmd/rune/ide/apiclient.DefaultWebsiteAddress=$(STAGING_WEBSITE_ADDRESS)
# Use `=` (recursive) so $(RUNE_ENV) is re-evaluated when RUNE_GOFLAGS is expanded
# in a recipe — this lets target-specific overrides such as
# `staging-dist-darwin-arm64: RUNE_ENV := staging` flip the ldflags for
# transitive prerequisites without affecting the default `make rune` build.
RUNE_GOFLAGS=$(RACE_FLAG) -tags=ebitensinglethread -ldflags="$(COMMON_LDFLAGS) -X unstable.build/rune/internal/debug.Package=rune $(if $(filter staging,$(RUNE_ENV)),$(STAGING_LDFLAGS))"

# GitHub repositories dist.sh uploads release tarballs / DMGs to.
# `prod-dist-*` targets publish to PROD_RELEASE_REPO (user-facing) while
# `staging-dist-*` targets publish to STAGING_RELEASE_REPO (internal).
PROD_RELEASE_REPO    ?= unstablebuild/rune
STAGING_RELEASE_REPO ?= unstablebuild/rune-staging

# The download host is derived from the repo: GitHub's
# `releases/latest/download` alias always resolves to the newest
# non-prerelease release, which is what stable clients follow.
PROD_DOWNLOAD_HOST    ?= https://github.com/$(PROD_RELEASE_REPO)/releases/latest/download
STAGING_DOWNLOAD_HOST ?= https://github.com/$(STAGING_RELEASE_REPO)/releases/latest/download

# Public www-rune origin baked into the binary for the OAuth callback
# redirect. Staging builds use the internal website; the default var in
# apiclient.DefaultWebsiteAddress already points at the user-facing site.
PROD_WEBSITE_ADDRESS     ?= https://rune.build
STAGING_WEBSITE_ADDRESS  ?= https://rune.unstable.build

# DIST_REPO / DIST_DOWNLOAD_HOST are set per-target by prod-dist-* /
# staging-dist-* and threaded into dist.sh via RELEASE_REPO and
# DOWNLOAD_HOST.
DIST_REPO          ?= $(PROD_RELEASE_REPO)
DIST_DOWNLOAD_HOST ?= $(PROD_DOWNLOAD_HOST)

TARGET := $(abspath $(ROOT_DIR)/target)
RUNE_RELEASE_DIR := $(TARGET)/rune_darwin_app
APP_NAME := Rune.app
APP_TEMPLATE := $(abspath $(ROOT_DIR)/extra/osx/$(APP_NAME))
APP_DIR := $(RUNE_RELEASE_DIR)
APP_BINARY := $(RUNE_RELEASE_DIR)/rune
APP_BINARY_DIR := $(APP_DIR)/$(APP_NAME)/Contents/MacOS
APP_EXTRAS_DIR := $(APP_DIR)/$(APP_NAME)/Contents/Resources
APP_PLIST := $(APP_DIR)/$(APP_NAME)/Contents/Info.plist
APP_PLIST_AMD64 := $(abspath $(ROOT_DIR)/extra/osx/Info.amd64.plist)
APP_ICON := $(APP_EXTRAS_DIR)/rune.icns
DMG_NAME := Rune.dmg
DMG_NAME_AMD64 := Rune-amd64.dmg
DMG_DIR := $(RUNE_RELEASE_DIR)
# DMG_STYLED toggles the styled create-dmg pipeline (background art, fixed
# window, branded volume icon, drag-to-Applications layout). Set to 0 to fall
# back to the plain `hdiutil create` path.
DMG_STYLED ?= 1
BUILD_DMG := $(abspath $(ROOT_DIR)/extra/osx/build-dmg.sh)
SED_INPLACE = ''

ifeq ($(OS),Darwin)
SED_INPLACE = ''
endif

.PHONY: clean make-release release release-amd64 release-arm64 app-amd64 app-arm64 dmg dmg-amd64 dmg-notarize dmg-amd64-notarize release-all \
	linux-cross-compile \
	make-release-linux \
	release-linux-amd64 release-linux-arm64 \
	release-linux-amd64-native release-linux-arm64-native \
	release-linux-amd64-cross release-linux-arm64-cross \
	check-release-tag \
	dist-linux-amd64 dist-linux-arm64 dist-darwin-arm64 dist-darwin-amd64 \
	dist-linux-amd64-native dist-linux-arm64-native \
	dist-linux-amd64-cross dist-linux-arm64-cross \
	prod-dist-linux-amd64 prod-dist-linux-arm64 prod-dist-darwin-amd64 prod-dist-darwin-arm64 \
	prod-dist-linux-amd64-native prod-dist-linux-arm64-native \
	prod-dist-linux-amd64-cross prod-dist-linux-arm64-cross \
	staging-dist-linux-amd64 staging-dist-linux-arm64 staging-dist-darwin-amd64 staging-dist-darwin-arm64 \
	staging-dist-linux-amd64-native staging-dist-linux-arm64-native \
	staging-dist-linux-amd64-cross staging-dist-linux-arm64-cross \
	app-delve

make-release: CGO_ENABLED=CGO_ENABLED=1
make-release:
	@mkdir -p $(TARGET)/rune_$(TARGET_OS)_$(TARGET_ARCH)
	@cd $(ROOT_DIR) && ./cmd/rune/cgo-build-guard.sh '$(CGO_ENABLED) GOARCH=$(TARGET_ARCH) $(TARGET_ARCH_FLAGS) GOOS=$(TARGET_OS) $(GO) build $(RUNE_GOFLAGS) -o $(TARGET)/rune_$(TARGET_OS)_$(TARGET_ARCH)/rune ./cmd/rune'
	@go_files=$$(find $(TARGET)/rune_$(TARGET_OS)_$(TARGET_ARCH) -name '*.go'); \
		if [ -n "$$go_files" ]; then \
			echo "error: .go files found in package:" >&2; \
			echo "$$go_files" >&2; \
			exit 1; \
		fi
ifeq ($(UNAME),Linux)
release:
	@rm -rf $(TARGET)/rune_linux_amd64
	@TARGET_OS=linux TARGET_ARCH=amd64 $(MAKE) make-release
	@cd $(TARGET) && tar -czvf rune-release-`git describe --tags --dirty`.tar.gz rune_linux_amd64

app-amd64:
	@echo "Skipping app packaging (not macOS)"

app-arm64:
	@echo "Skipping app packaging (not macOS)"

dmg:
	@echo "Skipping DMG packaging (not macOS)"

dmg-notarize: dmg
	@echo "Skipping DMG notarization (not macOS)"

release-all:
	@echo "Skipping release-all (not macOS)"
endif

ifeq ($(UNAME),Darwin)
release-amd64:
	@rm -rf $(TARGET)/rune_darwin_amd64
	@TARGET_OS=darwin TARGET_ARCH=amd64 TARGET_ARCH_FLAGS='$(DARWIN_AMD64_TARGET_ARCH_FLAGS)' $(MAKE) make-release
	@cd $(TARGET) && tar -czvf rune-release-`git describe --tags --dirty`.tar.gz rune_darwin_amd64

release-arm64:
	@rm -rf $(TARGET)/rune_darwin_arm64
	@TARGET_OS=darwin TARGET_ARCH=arm64 TARGET_ARCH_FLAGS='$(DARWIN_ARM64_TARGET_ARCH_FLAGS)' $(MAKE) make-release
	@cd $(TARGET) && tar -czvf rune-release-`git describe --tags --dirty`.tar.gz rune_darwin_arm64

release:
	@rm -rf $(TARGET)/rune_darwin_arm64 $(TARGET)/rune_darwin_amd64
	@TARGET_OS=darwin TARGET_ARCH=arm64 TARGET_ARCH_FLAGS='$(DARWIN_ARM64_TARGET_ARCH_FLAGS)' $(MAKE) make-release
	@TARGET_OS=darwin TARGET_ARCH=amd64 TARGET_ARCH_FLAGS='$(DARWIN_AMD64_TARGET_ARCH_FLAGS)' $(MAKE) make-release
	@cd $(TARGET) && tar -czvf rune-release-`git describe --tags --dirty`.tar.gz rune_darwin_arm64 rune_darwin_amd64

app-amd64:
	@rm -rf $(RUNE_RELEASE_DIR)
	@mkdir -p $(RUNE_RELEASE_DIR)
	@cd $(ROOT_DIR) && ./cmd/rune/cgo-build-guard.sh 'CGO_ENABLED=1 GOARCH=amd64 $(DARWIN_AMD64_TARGET_ARCH_FLAGS) GOOS=darwin $(GO) build $(RUNE_GOFLAGS) -o $(APP_BINARY) ./cmd/rune'
	@mkdir -p $(APP_BINARY_DIR) $(APP_EXTRAS_DIR)
	@cp -fRp $(APP_TEMPLATE) $(APP_DIR)
	@cp -fp $(APP_PLIST_AMD64) $(APP_PLIST)
	@cp -fp $(APP_BINARY) $(APP_BINARY_DIR)
	@ibtool --compile $(APP_EXTRAS_DIR)/MainMenu.nib $(APP_TEMPLATE)/Contents/Resources/MainMenu.xib
	@echo "injecting version $(VERSION) ($(COMMIT))"
	@sed -i $(SED_INPLACE) -e 's/{{VERSION}}/$(VERSION)/g' -e 's/{{COMMIT}}/$(COMMIT)/g' $(APP_PLIST)
	@cd $(ROOT_DIR)/extra && iconutil -c icns icon.iconset
	@mv $(ROOT_DIR)/extra/icon.icns $(APP_ICON)
	@touch -r "$(APP_BINARY)" "$(APP_DIR)/$(APP_NAME)"
	@rm -f $(APP_BINARY)
	@codesign --force --deep --options runtime --sign "$(CODESIGN_IDENTITY)" "$(APP_DIR)/$(APP_NAME)"
	@echo "Created '$(APP_NAME)' in '$(APP_DIR)'"

app-arm64:
	@rm -rf $(RUNE_RELEASE_DIR)
	@mkdir -p $(RUNE_RELEASE_DIR)
	@cd $(ROOT_DIR) && ./cmd/rune/cgo-build-guard.sh 'CGO_ENABLED=1 GOARCH=arm64 $(DARWIN_ARM64_TARGET_ARCH_FLAGS) GOOS=darwin $(GO) build $(RUNE_GOFLAGS) -o $(APP_BINARY) ./cmd/rune'
	@mkdir -p $(APP_BINARY_DIR) $(APP_EXTRAS_DIR)
	@cp -fRp $(APP_TEMPLATE) $(APP_DIR)
	@cp -fp $(APP_BINARY) $(APP_BINARY_DIR)
	@ibtool --compile $(APP_EXTRAS_DIR)/MainMenu.nib $(APP_TEMPLATE)/Contents/Resources/MainMenu.xib
	@echo "injecting version $(VERSION) ($(COMMIT))"
	@sed -i $(SED_INPLACE) -e 's/{{VERSION}}/$(VERSION)/g' -e 's/{{COMMIT}}/$(COMMIT)/g' $(APP_PLIST)
	@cd $(ROOT_DIR)/extra && iconutil -c icns icon.iconset
	@mv $(ROOT_DIR)/extra/icon.icns $(APP_ICON)
	@touch -r "$(APP_BINARY)" "$(APP_DIR)/$(APP_NAME)"
	@rm -f $(APP_BINARY)
	@codesign --force --deep --options runtime --sign "$(CODESIGN_IDENTITY)" "$(APP_DIR)/$(APP_NAME)"
	@echo "Created '$(APP_NAME)' in '$(APP_DIR)'"

dmg: app-arm64
	@echo "Packing disk image..."
	@mkdir -p $(DMG_DIR)
ifeq ($(DMG_STYLED),1)
	@$(BUILD_DMG) $(APP_DIR) $(DMG_DIR)/$(DMG_NAME) "Rune"
else
	@ln -sf /Applications $(DMG_DIR)/Applications
	@hdiutil create $(DMG_DIR)/$(DMG_NAME) \
		-volname "Rune" \
		-fs HFS+ \
		-srcfolder $(APP_DIR) \
		-ov -format UDZO
endif
	@echo "Signing disk image..."
	@codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(DMG_DIR)/$(DMG_NAME)
	@echo "Packed $(DMG_DIR)/$(DMG_NAME)"

dmg-amd64: app-amd64
	@echo "Packing amd64 disk image..."
	@mkdir -p $(DMG_DIR)
ifeq ($(DMG_STYLED),1)
	@$(BUILD_DMG) $(APP_DIR) $(DMG_DIR)/$(DMG_NAME_AMD64) "Rune"
else
	@ln -sf /Applications $(DMG_DIR)/Applications
	@hdiutil create $(DMG_DIR)/$(DMG_NAME_AMD64) \
		-volname "Rune" \
		-fs HFS+ \
		-srcfolder $(APP_DIR) \
		-ov -format UDZO
endif
	@echo "Signing amd64 disk image..."
	@codesign --force --options runtime --sign "$(CODESIGN_IDENTITY)" $(DMG_DIR)/$(DMG_NAME_AMD64)
	@echo "Packed $(DMG_DIR)/$(DMG_NAME_AMD64)"

dmg-notarize: dmg
	@echo "Notarizing disk image..."
	@set -e; \
	RESULT=$$(xcrun notarytool submit $(DMG_DIR)/$(DMG_NAME) --keychain-profile "$(NOTARY_PROFILE)" --wait --output-format json); \
	echo "$$RESULT"; \
	ID=$$(printf '%s\n' "$$RESULT" | plutil -extract id raw -o - -); \
	STATUS=$$(printf '%s\n' "$$RESULT" | plutil -extract status raw -o - -); \
	echo "Submission ID: $$ID"; \
	echo "Status: $$STATUS"; \
	if [ "$$STATUS" != "Accepted" ]; then \
		echo "Fetching notarization log..."; \
		xcrun notarytool log "$$ID" --keychain-profile "$(NOTARY_PROFILE)"; \
		exit 1; \
	fi
	@echo "Stapling ticket..."
	@xcrun stapler staple $(DMG_DIR)/$(DMG_NAME)
	@echo "Validating staple..."
	@xcrun stapler validate $(DMG_DIR)/$(DMG_NAME)
	@echo "Notarization completed: $(DMG_DIR)/$(DMG_NAME)"

dmg-amd64-notarize: dmg-amd64
	@echo "Notarizing amd64 disk image..."
	@set -e; \
	RESULT=$$(xcrun notarytool submit $(DMG_DIR)/$(DMG_NAME_AMD64) --keychain-profile "$(NOTARY_PROFILE)" --wait --output-format json); \
	echo "$$RESULT"; \
	ID=$$(printf '%s\n' "$$RESULT" | plutil -extract id raw -o - -); \
	STATUS=$$(printf '%s\n' "$$RESULT" | plutil -extract status raw -o - -); \
	echo "Submission ID: $$ID"; \
	echo "Status: $$STATUS"; \
	if [ "$$STATUS" != "Accepted" ]; then \
		echo "Fetching notarization log..."; \
		xcrun notarytool log "$$ID" --keychain-profile "$(NOTARY_PROFILE)"; \
		exit 1; \
	fi
	@echo "Stapling amd64 ticket..."
	@xcrun stapler staple $(DMG_DIR)/$(DMG_NAME_AMD64)
	@echo "Validating amd64 staple..."
	@xcrun stapler validate $(DMG_DIR)/$(DMG_NAME_AMD64)
	@echo "Notarization completed: $(DMG_DIR)/$(DMG_NAME_AMD64)"

release-all: dmg-notarize

# app-delve builds the macOS .app for the host arch with debugger-friendly
# flags (-gcflags="all=-N -l") and ad-hoc signs it with the
# com.apple.security.get-task-allow entitlement so delve can attach to the
# running PID. No hardened runtime, no notarization — strictly for local
# debugging.
#
# Usage:
#   make rune-app-delve
#   open $(RUNE_RELEASE_DIR)/Rune.app
#   dlv attach $$(pgrep -n rune)
app-delve: RUNE_DEBUG_BUILD := true
app-delve:
	@rm -rf $(RUNE_RELEASE_DIR)
	@mkdir -p $(RUNE_RELEASE_DIR)
	@cd $(ROOT_DIR) && CGO_ENABLED=1 GOOS=darwin $(GO) build -gcflags="all=-N -l" $(RUNE_GOFLAGS) -o $(APP_BINARY) ./cmd/rune
	@mkdir -p $(APP_BINARY_DIR) $(APP_EXTRAS_DIR)
	@cp -fRp $(APP_TEMPLATE) $(APP_DIR)
	@cp -fp $(APP_BINARY) $(APP_BINARY_DIR)
	@ibtool --compile $(APP_EXTRAS_DIR)/MainMenu.nib $(APP_TEMPLATE)/Contents/Resources/MainMenu.xib
	@echo "injecting version $(VERSION) ($(COMMIT))"
	@sed -i $(SED_INPLACE) -e 's/{{VERSION}}/$(VERSION)/g' -e 's/{{COMMIT}}/$(COMMIT)/g' $(APP_PLIST)
	@cd $(ROOT_DIR)/extra && iconutil -c icns icon.iconset
	@mv $(ROOT_DIR)/extra/icon.icns $(APP_ICON)
	@touch -r "$(APP_BINARY)" "$(APP_DIR)/$(APP_NAME)"
	@rm -f $(APP_BINARY)
	@printf '%s\n' \
		'<?xml version="1.0" encoding="UTF-8"?>' \
		'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
		'<plist version="1.0"><dict>' \
		'<key>com.apple.security.get-task-allow</key><true/>' \
		'</dict></plist>' > $(RUNE_RELEASE_DIR)/debug.entitlements
	@codesign --force --deep --sign - --entitlements $(RUNE_RELEASE_DIR)/debug.entitlements "$(APP_DIR)/$(APP_NAME)"
	@echo "Created debug-enabled '$(APP_NAME)' in '$(APP_DIR)'"
	@echo "Attach with: dlv attach \$$(pgrep -n rune)"
endif

linux-cross-compile:
	@rm -rf $(RUNE_LINUX_OUTPUT_DIR)
	@mkdir -p $(RUNE_LINUX_OUTPUT_DIR)
	@cd $(ROOT_DIR) && docker buildx build --rm \
		-f deploy/rune-linux/Dockerfile \
		--platform $(RUNE_LINUX_TARGET_OS)/$(RUNE_LINUX_TARGET_ARCH) \
		--build-arg GIT_SSH_KEY="$$GIT_SSH_KEY" \
		--build-arg RUNE_BUILD_TAG="$(VERSION)" \
		--build-arg RUNE_BUILD_COMMIT="$(COMMIT)" \
		--build-arg RUNE_BUILD_DATE="$(strip $(BUILD_DATE))" \
		--build-arg RUNE_RELEASE_TAR="$(RUNE_LINUX_RELEASE_TAR)" \
		--build-arg RUNE_ENV="$(RUNE_ENV)" \
		--output type=local,dest=$(RUNE_LINUX_OUTPUT_DIR) \
		.

# make-release-linux builds the rune.app bundle with the host's own toolchain
# (no Docker/QEMU), mirroring the layout produced by deploy/rune-linux/Dockerfile:
#   rune.app/bin/rune
#   rune.app/lib/*.so.*            (transitive NEEDED libs, rpath=$ORIGIN/../lib)
#   rune.app/share/applications/rune.desktop
#   rune.app/share/icons/hicolor/{512x512,1024x1024}/apps/rune.png
#   rune.app/share/zdot/.z*
# The bundling loop intentionally skips the glibc core, the C++ runtime
# (libstdc++/libgcc_s/libgomp), the OpenGL/GLX/EGL loader, and the X11/XCB
# client libs: those must come from the host. They are all forward-compatible,
# so the host's satisfy the binary. The binary no longer links the C++ runtime
# (llama.cpp is gone; inference runs in a separate llama-server process), so the
# libstdc++/libgcc_s/libgomp skip entries only guard transitive GUI deps and are
# harmless to keep. Bundling the GL or C++ runtime breaks the host GPU stack: the
# host's Mesa GLX vendor loads the host libstdc++, so shadowing it with an older
# bundled one (via rpath) makes glXGetFBConfigs return 0 ("GLX: No GLXFBConfigs
# returned").
# CGO is required (ebiten/GUI), so the binary links the host's shared libs; on a
# glibc host those are ABI-stable enough to ship. Native builds only support
# host-arch == target-arch; cross-arch must use the *-cross (Docker) targets.
make-release-linux:
	@if [ "$(UNAME)" != "Linux" ] || [ "$(HOST_ARCH)" != "$(RUNE_LINUX_TARGET_ARCH)" ]; then \
		echo "make-release-linux requires a Linux host whose arch matches RUNE_LINUX_TARGET_ARCH=$(RUNE_LINUX_TARGET_ARCH) (host: $(UNAME)/$(HOST_ARCH))."; \
		echo "Use the *-cross targets (e.g. release-linux-$(RUNE_LINUX_TARGET_ARCH)-cross) for cross-arch builds via Docker."; \
		exit 1; \
	fi
	@rm -rf $(RUNE_LINUX_OUTPUT_DIR)/rune.app
	@mkdir -p $(RUNE_LINUX_OUTPUT_DIR)/rune.app/bin $(RUNE_LINUX_OUTPUT_DIR)/rune.app/lib
	@cd $(ROOT_DIR) && CGO_ENABLED=1 CGO_LDFLAGS='-Wl,-rpath,$$ORIGIN/../lib' \
		GOOS=linux GOARCH=$(RUNE_LINUX_TARGET_ARCH) \
		$(GO) build $(RUNE_GOFLAGS) \
		-o $(RUNE_LINUX_OUTPUT_DIR)/rune.app/bin/rune ./cmd/rune
	@set -eu; \
	BIN=$(RUNE_LINUX_OUTPUT_DIR)/rune.app/bin/rune; \
	LIB=$(RUNE_LINUX_OUTPUT_DIR)/rune.app/lib; \
	TRIPLET=$$(gcc -dumpmachine); \
	objdump -p "$$BIN" | awk '$$1 == "NEEDED" {print $$2}' > "$$LIB/.queue"; \
	: > "$$LIB/.next"; \
	while [ -s "$$LIB/.queue" ]; do \
		: > "$$LIB/.next"; \
		while IFS= read -r soname; do \
			case "$$soname" in \
				libc.so*|libm.so*|libpthread.so*|libdl.so*|librt.so*|libresolv.so*|ld-linux*|linux-vdso*) continue ;; \
				libstdc++.so*|libgcc_s.so*|libgomp.so*) continue ;; \
				libGL.so*|libGLX.so*|libGLdispatch.so*|libEGL.so*|libOpenGL.so*|libGLU.so*|libgbm.so*|libdrm.so*) continue ;; \
				libX11.so*|libX11-xcb.so*|libxcb*.so*|libXext.so*|libXrandr.so*|libXrender.so*|libXcursor.so*|libXinerama.so*|libXi.so*|libXfixes.so*|libXdamage.so*|libXxf86vm.so*|libXau.so*|libXdmcp.so*|libxshmfence.so*) continue ;; \
			esac; \
			[ -f "$$LIB/$$soname" ] && continue; \
			src=""; \
			for d in /usr/lib/$$TRIPLET /lib/$$TRIPLET /usr/lib /lib; do \
				if [ -e "$$d/$$soname" ]; then src="$$d/$$soname"; break; fi; \
			done; \
			if [ -z "$$src" ]; then echo "WARN: $$soname not found"; continue; fi; \
			cp -L "$$src" "$$LIB/$$soname"; \
			echo "  bundled: $$soname"; \
			objdump -p "$$src" 2>/dev/null | awk '$$1 == "NEEDED" {print $$2}' >> "$$LIB/.next"; \
		done < "$$LIB/.queue"; \
		mv "$$LIB/.next" "$$LIB/.queue"; \
	done; \
	rm -f "$$LIB/.queue" "$$LIB/.next"
	@mkdir -p $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/applications \
		$(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/icons/hicolor/512x512/apps \
		$(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/icons/hicolor/1024x1024/apps \
		$(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/zdot
	@cp $(ROOT_DIR)/deploy/rune-linux/rune.desktop $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/applications/rune.desktop
	@cp $(ROOT_DIR)/extra/icon.iconset/icon_512x512.png $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/icons/hicolor/512x512/apps/rune.png
	@cp $(ROOT_DIR)/extra/icon.iconset/icon_512x512@2x.png $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/icons/hicolor/1024x1024/apps/rune.png
	@cp $(ROOT_DIR)/extra/osx/Rune.app/Contents/Resources/zdot/.zlogin $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/zdot/
	@cp $(ROOT_DIR)/extra/osx/Rune.app/Contents/Resources/zdot/.zprofile $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/zdot/
	@cp $(ROOT_DIR)/extra/osx/Rune.app/Contents/Resources/zdot/.zshenv $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/zdot/
	@cp $(ROOT_DIR)/extra/osx/Rune.app/Contents/Resources/zdot/.zshrc $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/zdot/
	@cp $(ROOT_DIR)/extra/osx/Rune.app/Contents/Resources/LICENSES $(RUNE_LINUX_OUTPUT_DIR)/rune.app/share/LICENSES
	@cd $(RUNE_LINUX_OUTPUT_DIR) && tar --format=ustar -czf $(RUNE_LINUX_RELEASE_TAR) rune.app

# release-linux-{amd64,arm64} build the portable release tarball through the
# Docker cross-compile path (debian:buster-slim, glibc 2.28 floor) so the
# artifact runs on any glibc >= 2.28 host regardless of the build machine.
# Use the -native variants to build with the host's own toolchain instead
# (host-arch must match target-arch; the floor becomes the build host's glibc).
release-linux-amd64:
	@RUNE_LINUX_TARGET_ARCH=amd64 RUNE_LINUX_RELEASE_TAR=rune-release-linux-amd64-$$(git describe --tags --dirty).tar.gz RUNE_ENV=$(RUNE_ENV) $(MAKE) linux-cross-compile

release-linux-arm64:
	@RUNE_LINUX_TARGET_ARCH=arm64 RUNE_LINUX_RELEASE_TAR=rune-release-linux-arm64-$$(git describe --tags --dirty).tar.gz RUNE_ENV=$(RUNE_ENV) $(MAKE) linux-cross-compile

release-linux-amd64-native:
	@RUNE_LINUX_TARGET_ARCH=amd64 RUNE_LINUX_RELEASE_TAR=rune-release-linux-amd64-$$(git describe --tags --dirty).tar.gz RUNE_ENV=$(RUNE_ENV) $(MAKE) make-release-linux

release-linux-arm64-native:
	@RUNE_LINUX_TARGET_ARCH=arm64 RUNE_LINUX_RELEASE_TAR=rune-release-linux-arm64-$$(git describe --tags --dirty).tar.gz RUNE_ENV=$(RUNE_ENV) $(MAKE) make-release-linux

# release-linux-*-cross are retained as aliases for the now-default cross build.
release-linux-amd64-cross: release-linux-amd64
release-linux-arm64-cross: release-linux-arm64

# dist-* are the underlying upload rules. They build the artifact via
# their prerequisite (release-linux-* cross build or DMG-notarize) and
# publish to $(DIST_REPO). They do NOT pin RUNE_ENV or DIST_REPO
# themselves — callers (prod-dist-* / staging-dist-*) set those via
# target-specific variables which propagate to all prerequisites in the
# same make invocation.
#
# check-release-tag is listed first so the tag is validated before any
# expensive build/notarize work runs. Prerequisites are built
# left-to-right (these targets are invoked serially, not with -j), so a
# bad tag fails fast instead of after a full build+notarize cycle.
dist-linux-amd64: check-release-tag release-linux-amd64
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune_linux_amd64/rune-release-linux-amd64-$$(git describe --tags --dirty).tar.gz \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=amd64 \
		RUNE_MIN_GLIBC=$(RUNE_MIN_GLIBC) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

dist-linux-arm64: check-release-tag release-linux-arm64
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune_linux_arm64/rune-release-linux-arm64-$$(git describe --tags --dirty).tar.gz \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=arm64 \
		RUNE_MIN_GLIBC=$(RUNE_MIN_GLIBC) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

# dist-linux-*-native mirror dist-linux-* but build the tarball with the
# host's own toolchain (make-release-linux) instead of the Docker cross
# path. The tarball path is identical, so dist.sh is invoked the same way.
dist-linux-amd64-native: check-release-tag release-linux-amd64-native
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune_linux_amd64/rune-release-linux-amd64-$$(git describe --tags --dirty).tar.gz \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=amd64 \
		RUNE_MIN_GLIBC=$(RUNE_MIN_GLIBC) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

dist-linux-arm64-native: check-release-tag release-linux-arm64-native
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(TARGET)/rune_linux_arm64/rune-release-linux-arm64-$$(git describe --tags --dirty).tar.gz \
		BLUE_TARGET_OS=linux \
		BLUE_TARGET_ARCH=arm64 \
		RUNE_MIN_GLIBC=$(RUNE_MIN_GLIBC) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

# dist-linux-*-cross are retained as aliases for the now-default cross build.
dist-linux-amd64-cross: dist-linux-amd64
dist-linux-arm64-cross: dist-linux-arm64

dist-darwin-arm64: check-release-tag dmg-notarize
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(DMG_DIR)/$(DMG_NAME) \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=arm64 \
		RUNE_MIN_MACOS=$(DARWIN_ARM64_MIN_MACOS) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

dist-darwin-amd64: check-release-tag dmg-amd64-notarize
	@cd $(ROOT_DIR) && \
		BLUE_RELEASE_TAR=$(DMG_DIR)/$(DMG_NAME_AMD64) \
		BLUE_TARGET_OS=darwin \
		BLUE_TARGET_ARCH=amd64 \
		RUNE_MIN_MACOS=$(DARWIN_AMD64_MIN_MACOS) \
		RELEASE_REPO=$(DIST_REPO) \
		DOWNLOAD_HOST=$(DIST_DOWNLOAD_HOST) \
		./cmd/rune/dist.sh

# check-release-tag aborts before any build runs when HEAD does not
# carry a publishable release tag. Shares cmd/rune/check-release-tag.sh
# with dist.sh so the rules cannot drift.
check-release-tag:
	@cd $(ROOT_DIR) && ./cmd/rune/check-release-tag.sh

# prod-dist-*: build with the default prod endpoints
# (api.rune.build / rpc.rune.build:443) and upload to
# $(PROD_RELEASE_REPO).
prod-dist-linux-amd64:  RUNE_ENV := prod
prod-dist-linux-amd64:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-amd64:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-amd64:  dist-linux-amd64

prod-dist-linux-arm64:  RUNE_ENV := prod
prod-dist-linux-arm64:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-arm64:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-arm64:  dist-linux-arm64

prod-dist-linux-amd64-native:  RUNE_ENV := prod
prod-dist-linux-amd64-native:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-amd64-native:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-amd64-native:  dist-linux-amd64-native

prod-dist-linux-arm64-native:  RUNE_ENV := prod
prod-dist-linux-arm64-native:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-arm64-native:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-arm64-native:  dist-linux-arm64-native

# prod-dist-linux-*-cross are retained as aliases for the now-default cross build.
prod-dist-linux-amd64-cross:  RUNE_ENV := prod
prod-dist-linux-amd64-cross:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-amd64-cross:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-amd64-cross:  dist-linux-amd64

prod-dist-linux-arm64-cross:  RUNE_ENV := prod
prod-dist-linux-arm64-cross:  DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-linux-arm64-cross:  DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-linux-arm64-cross:  dist-linux-arm64

prod-dist-darwin-amd64: RUNE_ENV := prod
prod-dist-darwin-amd64: DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-darwin-amd64: DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-darwin-amd64: dist-darwin-amd64

prod-dist-darwin-arm64: RUNE_ENV := prod
prod-dist-darwin-arm64: DIST_REPO := $(PROD_RELEASE_REPO)
prod-dist-darwin-arm64: DIST_DOWNLOAD_HOST := $(PROD_DOWNLOAD_HOST)
prod-dist-darwin-arm64: dist-darwin-arm64

# staging-dist-*: build with the staging ldflags
# (api.unstable.build / rpc.unstable.build:443) and upload to
# $(STAGING_RELEASE_REPO).
staging-dist-linux-amd64:  RUNE_ENV := staging
staging-dist-linux-amd64:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-amd64:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-amd64:  dist-linux-amd64

staging-dist-linux-arm64:  RUNE_ENV := staging
staging-dist-linux-arm64:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-arm64:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-arm64:  dist-linux-arm64

staging-dist-linux-amd64-native:  RUNE_ENV := staging
staging-dist-linux-amd64-native:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-amd64-native:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-amd64-native:  dist-linux-amd64-native

staging-dist-linux-arm64-native:  RUNE_ENV := staging
staging-dist-linux-arm64-native:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-arm64-native:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-arm64-native:  dist-linux-arm64-native

# staging-dist-linux-*-cross are retained as aliases for the now-default cross build.
staging-dist-linux-amd64-cross:  RUNE_ENV := staging
staging-dist-linux-amd64-cross:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-amd64-cross:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-amd64-cross:  dist-linux-amd64

staging-dist-linux-arm64-cross:  RUNE_ENV := staging
staging-dist-linux-arm64-cross:  DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-linux-arm64-cross:  DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-linux-arm64-cross:  dist-linux-arm64

staging-dist-darwin-amd64: RUNE_ENV := staging
staging-dist-darwin-amd64: DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-darwin-amd64: DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-darwin-amd64: dist-darwin-amd64

staging-dist-darwin-arm64: RUNE_ENV := staging
staging-dist-darwin-arm64: DIST_REPO := $(STAGING_RELEASE_REPO)
staging-dist-darwin-arm64: DIST_DOWNLOAD_HOST := $(STAGING_DOWNLOAD_HOST)
staging-dist-darwin-arm64: dist-darwin-arm64

clean:
	@rm -rf $(RUNE_RELEASE_DIR)
