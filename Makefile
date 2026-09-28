GO=go
CI ?= false
# The skip-on-CI guards in the test suites read this with os.Getenv, so the
# recipes need it in their environment. Export it once rather than per target:
# `test: CI=$(CI)` self-references once CI already comes from the environment,
# as it does on GitHub Actions, and make refuses to expand it.
export CI
# Per test binary, not a whole-suite budget: it exists to dump goroutines when a
# package hangs, so it should stay close to the slowest legitimate run. The
# median package takes ~5s and p95 ~34s, and the slowest (internal/ide, cmd/rune)
# sit at ~85-100s. 180s left those under 2x and timed them out on a loaded
# machine, which then skipped their t.Cleanup and leaked docker containers,
# slowing the host and causing the next timeout.
GOTESTFLAGS ?= -race -timeout 300s
GOTESTFLAGSNORACE = -timeout 300s
# The e2e suites are slower than anything in `make test` (~120s for the docker
# containers) and the first run also builds the sshd image.
E2E_GOTESTFLAGS ?= -race -timeout 900s
# RUNE_DEBUG_BUILD, when set to "true", flips an in-binary feature
# flag that exposes debug-only ex commands such as :panic and :crash.
# Defaults to off; the `debug` target sets it via a target-specific
# variable. Recursive (=) so target-specific overrides propagate to
# prerequisites that re-expand COMMON_LDFLAGS.
RUNE_DEBUG_BUILD ?=
DEBUG_LDFLAGS=$(if $(filter true,$(RUNE_DEBUG_BUILD)),-X unstable.build/rune/internal/debug.DebugBuild=true)
# RACE_FLAG mirrors RUNE_DEBUG_BUILD so the race detector is enabled
# for every binary the `debug` target produces, including the rune
# binary (which builds with RUNE_GOFLAGS rather than GOFLAGS). This is
# what makes :datarace actually crash the debug build.
RACE_FLAG=$(if $(filter true,$(RUNE_DEBUG_BUILD)),-race)
# REPO_ROOT anchors the buildstamp invocation to the module root so it
# resolves no matter what cwd a recipe runs from (build recipes cd into
# cmd/rune before expanding these flags, so a bare ./cmd/buildstamp
# would look under cmd/rune and fail). It is derived from this
# makefile's own path rather than `git rev-parse --show-toplevel` so it
# stays in the same symlink namespace as the shell's cwd: a clone under
# /tmp on macOS resolves to /private/tmp, which `go run` then rejects as
# "outside main module".
REPO_ROOT := $(patsubst %/,%,$(dir $(abspath $(firstword $(MAKEFILE_LIST)))))
# BUILD_DATE renders the build time as RFC3339 UTC via cmd/buildstamp so
# Go, not the host's date(1), formats it and the debug.BuildDate ldflag
# is identical across platforms. The `out=$(...) && printf` guard emits
# the stamp only when buildstamp exits zero, so any failure (compile
# error, panic, non-zero exit even after printing a partial line) leaves
# BUILD_DATE empty; buildstamp's error still reaches stderr. BUILD_DATE_LDFLAG
# then $(error)s on an empty stamp so a broken buildstamp aborts the build
# instead of shipping a release binary whose `rune --version` reports no
# build date. The guard lives in the ldflag (not at parse time) so
# non-build targets like `clean` are unaffected; an inline $$(...)
# substitution could not enforce this because its non-zero exit would not
# fail the surrounding go build.
BUILD_DATE := $(shell out=$$(cd $(REPO_ROOT) && $(GO) run ./cmd/buildstamp) && printf '%s' "$$out")
BUILD_DATE_LDFLAG = $(if $(strip $(BUILD_DATE)),,$(error buildstamp produced no build date; refusing to build a binary with an empty debug.BuildDate))-X unstable.build/rune/internal/debug.BuildDate=$(strip $(BUILD_DATE))
# dist/arch/PKGBUILD and dist/debian/debian/rules re-declare this set
# because makepkg and dpkg-buildpackage drive the compiler themselves and
# never call these rules. A flag added or renamed here has to be mirrored
# in both, or packaged builds quietly ship without it.
COMMON_LDFLAGS=-X unstable.build/rune/internal/debug.Tag=$$(git describe --tags) -X unstable.build/rune/internal/debug.Commit=$$(git rev-parse --short HEAD) $(BUILD_DATE_LDFLAG) $(DEBUG_LDFLAGS)
GOFLAGS=$(RACE_FLAG) -ldflags="$(COMMON_LDFLAGS) -X unstable.build/rune/internal/debug.Package=six"
RUNE_GOFLAGS=$(RACE_FLAG) -tags=ebitensinglethread -ldflags="$(COMMON_LDFLAGS) -X unstable.build/rune/internal/debug.Package=rune"
UNAME := $(shell uname)
VERSION=$(shell git describe --tags)
COMMIT=$(shell git rev-parse --short HEAD)
CODESIGN_IDENTITY ?= Developer ID Application: Unstable Build, LLC. (YYZRWD888J)
NOTARY_PROFILE ?= notary-profile

BIN=bin
TARGET=target
LIBSRC=$(wildcard *.go) $(wildcard **/*.go) $(wildcard **/**/*.go) $(wildcard **/**/**/*.go)
EXECSRC=$(wildcard cmd/**/*.go) $(wildcard cmd/**/**/*.go)
EXECMAIN=$(wildcard cmd/*/main.go)
EXECDIRS=$(sort $(dir $(EXECMAIN)))
EXECS=$(patsubst cmd/%/,$(BIN)/%,$(EXECDIRS))
SPECIAL_EXECS=$(BIN)/rune $(BIN)/rune-agent
GENERIC_EXECS=$(filter-out $(SPECIAL_EXECS),$(EXECS))
EXEC_PKGS=$(patsubst $(BIN)/%,./cmd/%,$(EXECS))
RELEASE_EXEC_PKGS=$(EXEC_PKGS)
GOMOCKS=$(wildcard **/**/*_gomock.go) $(wildcard **/*_gomock.go)
RELEASE_FILES=$(wildcard release/*)
.PHONY: debug clean test test-e2e coverage generate rune rune-agent \
	format cross-compile lint license assert_license dist \
	rune-release rune-release-amd64 rune-release-arm64 rune-make-release \
	rune-app-delve \
	rune-linux-cross-compile rune-app-amd64 rune-app-arm64 \
	rune-staging-app-arm64 \
	rune-dmg rune-dmg-amd64 rune-dmg-notarize rune-dmg-amd64-notarize rune-release-all \
	rune-agent-pkg rune-agent-sign rune-agent-notarize \
	rune-agent-prod-dist rune-agent-staging-dist \
	rune-agent-prod-dist-notarized rune-agent-staging-dist-notarized \
	rune-agent-linux-cross-compile \
	rune-agent-release-linux-amd64 rune-agent-release-linux-arm64 \
	rune-agent-release-linux-amd64-cross rune-agent-release-linux-arm64-cross \
	rune-agent-prod-dist-linux-amd64 rune-agent-staging-dist-linux-amd64 \
	rune-agent-prod-dist-linux-arm64 rune-agent-staging-dist-linux-arm64 \
	rune-agent-prod-dist-linux-amd64-cross rune-agent-staging-dist-linux-amd64-cross \
	rune-agent-prod-dist-linux-arm64-cross rune-agent-staging-dist-linux-arm64-cross \
	rune-agent-prod-dist-darwin-amd64 rune-agent-staging-dist-darwin-amd64 \
	rune-agent-prod-dist-darwin-arm64 rune-agent-staging-dist-darwin-arm64 \
	fuzzy-search fuzzy-search-pkg \
	fuzzy-search-prod-dist fuzzy-search-staging-dist \
	fuzzy-search-linux-cross-compile \
	fuzzy-search-release-linux-amd64 fuzzy-search-release-linux-arm64 \
	fuzzy-search-release-linux-amd64-cross fuzzy-search-release-linux-arm64-cross \
	fuzzy-search-prod-dist-linux-amd64 fuzzy-search-staging-dist-linux-amd64 \
	fuzzy-search-prod-dist-linux-arm64 fuzzy-search-staging-dist-linux-arm64 \
	fuzzy-search-prod-dist-linux-amd64-cross fuzzy-search-staging-dist-linux-amd64-cross \
	fuzzy-search-prod-dist-linux-arm64-cross fuzzy-search-staging-dist-linux-arm64-cross \
	fuzzy-search-prod-dist-darwin-amd64 fuzzy-search-staging-dist-darwin-amd64 \
	fuzzy-search-prod-dist-darwin-arm64 fuzzy-search-staging-dist-darwin-arm64 \
	runectl-pkg runectl-sign runectl-notarize \
	runectl-prod-dist runectl-staging-dist \
	runectl-prod-dist-notarized runectl-staging-dist-notarized \
	runectl-prod-dist-linux-amd64 runectl-staging-dist-linux-amd64 \
	runectl-prod-dist-linux-arm64 runectl-staging-dist-linux-arm64 \
	runectl-prod-dist-darwin-amd64 runectl-staging-dist-darwin-amd64 \
	runectl-prod-dist-darwin-arm64 runectl-staging-dist-darwin-arm64 \
	notary-credentials runectl \
	rune-release-linux-amd64 rune-release-linux-arm64 \
	rune-release-linux-amd64-native rune-release-linux-arm64-native \
	rune-release-linux-amd64-cross rune-release-linux-arm64-cross \
	rune-prod-dist-linux-amd64 rune-prod-dist-linux-arm64 \
	rune-prod-dist-linux-amd64-native rune-prod-dist-linux-arm64-native \
	rune-prod-dist-linux-amd64-cross rune-prod-dist-linux-arm64-cross \
	rune-prod-dist-darwin-arm64 rune-prod-dist-darwin-amd64 \
	rune-staging-dist-linux-amd64 rune-staging-dist-linux-arm64 \
	rune-staging-dist-linux-amd64-native rune-staging-dist-linux-arm64-native \
	rune-staging-dist-linux-amd64-cross rune-staging-dist-linux-arm64-cross \
	rune-staging-dist-darwin-arm64 rune-staging-dist-darwin-amd64 \
	fuzz fuzz-list \
	FORCE \
	manual-ssh-test \
	dist-tar-with-src dist-dmg-with-src dist-min-macos dist-min-linux \
	pkg-deb pkg-deb-amd64 pkg-deb-arm64 pkg-deb-docker \
	pkg-arch pkg-arch-srcinfo pkg-clean \
	$(filter internal/workspace/workspacessh/manual_test/%.sh,$(MAKECMDGOALS))

# bluectl config matrix. Each leaf config pins BOTH auth.project-id and
# release.collection so the publishing env + destination bucket are
# selected by the make target rather than by ~/.bluectl/config or
# whatever bucket the user's local config happens to name.
#
# The host targets (rune-agent-{prod,staging}-dist, runectl-*, fuzzy-*)
# derive their os/arch from the host running make.
BLUECTL_HOST_OS   := $(shell uname | awk '{print tolower($$0)}')
BLUECTL_HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
BLUECTL_CONFIG_ROOT := $(abspath deploy/bluectl)

# Helper: resolve a leaf config dir given env (prod|staging) and os-arch slug.
BLUECTL_CONFIG = $(BLUECTL_CONFIG_ROOT)/$(1)/$(2)

# Inside a worktree `.git` is a file and the hooks live in the common dir, so a
# literal .git/hooks/... target can never be satisfied and every build reruns
# the install.
GIT_HOOKS := $(shell git rev-parse --git-path hooks 2>/dev/null)

default: CGO_ENABLED=CGO_ENABLED=1
default: GOPRIVATE=github.com/unstablebuild,unstable.build/*
default: $(if $(GIT_HOOKS),$(GIT_HOOKS)/pre-commit) $(EXECS)

debug: RUNE_DEBUG_BUILD := true
debug: CGO_ENABLED=CGO_ENABLED=1
debug: GOPRIVATE=github.com/unstablebuild,unstable.build/*
debug: $(EXECS)

rune: CGO_ENABLED=CGO_ENABLED=1
rune: GOPRIVATE=github.com/unstablebuild,unstable.build/*
rune: $(BIN)/rune

rune-agent: CGO_ENABLED=CGO_ENABLED=1
rune-agent: GOPRIVATE=github.com/unstablebuild,unstable.build/*
rune-agent: $(BIN)/rune-agent

# One install covers both hook types (default_install_hook_types). Two
# concurrent installs race writing these files and can strand a valid hook as
# pre-commit.legacy, which makes every later commit abort in migration mode.
$(GIT_HOOKS)/pre-commit: .pre-commit-config.yaml
	@ command -v pre-commit >/dev/null 2>&1 && pre-commit install -f \
		|| echo "pre-commit not installed; skipping git hook setup"

test:
	@ go test -vet=off ./.../... $(GOTESTFLAGS)

# Runs the whole suite including the tests that drive real external processes
# (docker containers, a shell in a pty). Those sit behind the e2e build tag so
# `make test` stays hermetic and is not exposed to their timing sensitivity.
test-e2e:
	@ go test -vet=off -tags e2e ./.../... $(E2E_GOTESTFLAGS)

test-no-race:
	@ go test ./.../... $(GOTESTFLAGSNORACE)

coverage: $(BIN)
	@ go test ./.../... -coverprofile $(BIN)/coverage
	@ go tool cover -html=$(BIN)/coverage

# fuzz runs every `Fuzz*` target in the repository for FUZZTIME each.
# Each target runs sequentially with -parallel=1 to keep resource usage
# bounded.
#
# Override defaults on the command line:
#   make fuzz FUZZTIME=30s         # longer per-target budget
#   make fuzz FUZZ_PKG=./internal/component/markdown/...
FUZZTIME ?= 10s
FUZZ_PKG ?= ./...
FUZZ_TEST_FLAGS ?= -race -parallel=1 -count=1

fuzz:
	@ set -e; \
	pkgs=$$(go list -f '{{if (or .TestGoFiles .XTestGoFiles)}}{{.ImportPath}}{{end}}' $(FUZZ_PKG)); \
	for pkg in $$pkgs; do \
		targets=$$(go test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); \
		[ -z "$$targets" ] && continue; \
		for t in $$targets; do \
			echo "==> $$pkg $$t (-fuzztime=$(FUZZTIME))"; \
			go test $(FUZZ_TEST_FLAGS) -run='^$$' -fuzz='^'"$$t"'$$' -fuzztime=$(FUZZTIME) $$pkg || exit $$?; \
		done; \
	done

# fuzz-list prints every fuzz target the repository ships, grouped by
# package. Useful when you want to invoke `go test -fuzz=…` directly.
fuzz-list:
	@ set -e; \
	pkgs=$$(go list -f '{{if (or .TestGoFiles .XTestGoFiles)}}{{.ImportPath}}{{end}}' ./...); \
	for pkg in $$pkgs; do \
		targets=$$(go test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); \
		[ -z "$$targets" ] && continue; \
		echo "$$pkg:"; \
		for t in $$targets; do echo "  $$t"; done; \
	done

# bench-gui runs the end-to-end GUI renderer benchmark battery
# (BenchmarkGUI in cmd/rune) and writes the results to
# benchmarks/results/<git-sha>.txt so runs can be compared across
# commits with benchstat. Override BENCH_COUNT / BENCHTIME as needed:
#   make bench-gui BENCH_COUNT=6 BENCHTIME=30x
# Compare two runs:
#   benchstat benchmarks/results/<old>.txt benchmarks/results/<new>.txt
# See benchmarks/README.md for how to interpret the reported metrics
# and the fidelity caveats of the harness.
BENCH_COUNT ?= 10
BENCHTIME   ?= 50x
BENCH_GUI_OUT ?= benchmarks/results/$(shell git rev-parse --short HEAD).txt
bench-gui:
	@ mkdir -p benchmarks/results
	@ echo "==> BenchmarkGUI -> $(BENCH_GUI_OUT)"
	@ go test -run '^$$' -bench '^BenchmarkGUI$$' -benchmem \
		-benchtime=$(BENCHTIME) -count=$(BENCH_COUNT) -timeout=0 \
		./cmd/rune/ 2>/dev/null | tee $(BENCH_GUI_OUT)

generate: GOPRIVATE=github.com/unstablebuild,unstable.build/*
generate:
	@ rm -rf **/*rpc*/*.pb.go
	@ go generate ./...

license:
	@ bluectl license LICENSE_HEADER `find . -name \*.go -not -path ./cmd/rune/docs/\* | grep -v gomock | grep -v .pb.go | xargs`

assert_license:
	@ bluectl license -d LICENSE_HEADER `find . -name \*.go -not -path ./cmd/rune/docs/\* | grep -v gomock | grep -v .pb.go | xargs`

format:
	@ go fmt ./.../...

cross-compile:
	@ . ./test_crosscompile.sh

lint:
	@ golangci-lint run --timeout=600s

clean:
	@rm -rf $(BIN) $(TARGET)
	@$(MAKE) -C cmd/rune-agent clean
	@$(MAKE) -C cmd/extension_fuzzy_search clean
	@$(MAKE) -C cmd/runectl clean
	@$(MAKE) -C cmd/rune clean

$(BIN):
	@mkdir $(BIN)

$(BIN)/rune: $(EXECSRC) $(LIBSRC) $(BIN)
	@cd cmd/rune && $(CGO_ENABLED) $(GO) build $(RUNE_GOFLAGS) -o ../../$@

$(BIN)/rune-agent: $(EXECSRC) $(LIBSRC) $(BIN)
	@cd cmd/rune-agent && $(CGO_ENABLED) $(GO) build $(GOFLAGS) -o ../../$@

$(GENERIC_EXECS): $(EXECSRC) $(LIBSRC) $(BIN)
	cd $(patsubst bin/%,cmd/%,$@) && $(CGO_ENABLED) $(GO) build $(GOFLAGS) -o ../../$@

$(BIN)/runectl: $(BIN)
	@GOBIN="`pwd`/$(BIN)" $(GO) install github.com/unstablebuild/rune-go-sdk/cmd/runectl

make_release: CGO_ENABLED=CGO_ENABLED=1
make_release:
	@ mkdir -p $(TARGET)/$(TARGET_OS)_$(TARGET_ARCH)
	@ cp $(RELEASE_FILES) $(TARGET)
	@ $(CGO_ENABLED) GOARCH=$(TARGET_ARCH) $(TARGET_ARCH_FLAGS) GOOS=$(TARGET_OS) $(GO) build -o `pwd`/$(TARGET)/$(TARGET_OS)_$(TARGET_ARCH) $(GOFLAGS) $(RELEASE_EXEC_PKGS)

ifeq ($(UNAME), Linux)
release: default
	@ rm -rf $(TARGET)
	@ TARGET_OS=linux TARGET_ARCH=amd64 $(MAKE) make_release
	@ cd $(TARGET) && tar -czvf six-release-`git describe --tags --dirty`.tar.gz *
endif
ifeq ($(UNAME), Darwin)
release: default
	@ rm -rf $(TARGET)
	@ TARGET_OS=darwin TARGET_ARCH=arm64 $(MAKE) make_release
	@ TARGET_OS=darwin TARGET_ARCH=amd64 $(MAKE) make_release
	@ cd $(TARGET) && tar -czvf six-release-`git describe --tags --dirty`.tar.gz *
endif

dist: clean release
	@ git fetch origin --tags
	@ ./dist.sh

rune-make-release:
	@$(MAKE) -C cmd/rune make-release TARGET_OS="$(TARGET_OS)" TARGET_ARCH="$(TARGET_ARCH)" TARGET_ARCH_FLAGS="$(TARGET_ARCH_FLAGS)"

rune-release:
	@$(MAKE) -C cmd/rune release

rune-release-amd64:
	@$(MAKE) -C cmd/rune release-amd64

rune-release-arm64:
	@$(MAKE) -C cmd/rune release-arm64

FORCE:

rune-linux-cross-compile:
	@$(MAKE) -C cmd/rune linux-cross-compile

rune-release-linux-amd64:
	@$(MAKE) -C cmd/rune release-linux-amd64

rune-release-linux-arm64:
	@$(MAKE) -C cmd/rune release-linux-arm64

rune-release-linux-amd64-native:
	@$(MAKE) -C cmd/rune release-linux-amd64-native

rune-release-linux-arm64-native:
	@$(MAKE) -C cmd/rune release-linux-arm64-native

rune-release-linux-amd64-cross:
	@$(MAKE) -C cmd/rune release-linux-amd64-cross

rune-release-linux-arm64-cross:
	@$(MAKE) -C cmd/rune release-linux-arm64-cross

# rune-prod-dist-* / rune-staging-dist-*: build a release artifact and
# publish it as an asset on the corresponding GitHub release.
#
#   prod    -> unstablebuild/rune          (api.rune.build / rpc.rune.build:443)
#   staging -> unstablebuild/rune-staging  (api.unstable.build / rpc.unstable.build:443)
#
# Prod is the source-level default, so only the staging targets inject
# endpoint ldflags (RUNE_ENV=staging).
rune-prod-dist-linux-amd64: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-amd64

rune-prod-dist-linux-arm64: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-arm64

rune-prod-dist-linux-amd64-native: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-amd64-native

rune-prod-dist-linux-arm64-native: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-arm64-native

rune-prod-dist-linux-amd64-cross: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-amd64-cross

rune-prod-dist-linux-arm64-cross: clean
	@$(MAKE) -C cmd/rune prod-dist-linux-arm64-cross

rune-prod-dist-darwin-arm64: clean
	@$(MAKE) -C cmd/rune prod-dist-darwin-arm64

rune-prod-dist-darwin-amd64: clean
	@$(MAKE) -C cmd/rune prod-dist-darwin-amd64

rune-staging-dist-linux-amd64: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-amd64

rune-staging-dist-linux-arm64: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-arm64

rune-staging-dist-linux-amd64-native: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-amd64-native

rune-staging-dist-linux-arm64-native: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-arm64-native

rune-staging-dist-linux-amd64-cross: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-amd64-cross

rune-staging-dist-linux-arm64-cross: clean
	@$(MAKE) -C cmd/rune staging-dist-linux-arm64-cross

rune-staging-dist-darwin-arm64: clean
	@$(MAKE) -C cmd/rune staging-dist-darwin-arm64

rune-staging-dist-darwin-amd64: clean
	@$(MAKE) -C cmd/rune staging-dist-darwin-amd64

# dist-tar-with-src / dist-dmg-with-src exercise the .go-source publish
# gate (cmd/verify-no-go-source.sh) by driving every component's dist.sh
# against a stub artifact that intentionally embeds a .go file and
# asserting each script aborts before publishing. dist-dmg-with-src is a
# no-op skip off macOS (needs hdiutil).
dist-tar-with-src:
	@./cmd/dist-with-src-test.sh tar

dist-dmg-with-src:
	@./cmd/dist-with-src-test.sh dmg

# dist-min-macos exercises the minimum-macOS publish gate
# (cmd/verify-min-macos.sh + cmd/rune/dist.sh): it builds stub artifacts
# with a known Mach-O minos and asserts the gate refuses to publish one
# whose floor exceeds what we advertise, and fails closed when the floor
# is unset. macOS only (needs clang + otool); a no-op skip elsewhere.
dist-min-macos:
	@./cmd/dist-min-macos-test.sh

# dist-min-linux exercises the minimum-Linux publish gate
# (cmd/verify-min-linux.sh + cmd/rune/dist.sh): it packs a stub ELF
# artifact and asserts the gate refuses to publish one that requires a
# glibc version above the floor we advertise, and fails
# closed when the floor is unset. Linux only (needs a C compiler +
# file); a no-op skip elsewhere.
dist-min-linux:
	@./cmd/dist-min-linux-test.sh

# manual-ssh-test runs a named manual SSH scenario script using the
# real Linux release rune binary as the remote workspace server.
#
# We pick the linux arch that matches the docker host's native
# architecture so the openssh test container runs the rune binary
# without QEMU emulation (the harness's e2e tests do the same; see
# containerGOARCH() in internal/workspace/workspacessh/test/harness.go).
#
# Usage:
#   make manual-ssh-test internal/workspace/workspacessh/manual_test/01_host_key_match.sh
RUNE_LINUX_HOST_ARCH := $(shell uname -m | sed -e 's/^arm64$$/arm64/' -e 's/^aarch64$$/arm64/' -e 's/^x86_64$$/amd64/' -e 's/^amd64$$/amd64/')
RUNE_LINUX_REMOTE_BIN := $(TARGET)/rune_linux_$(RUNE_LINUX_HOST_ARCH)/rune.app/bin/rune
MANUAL_SSH_SCRIPT := $(filter internal/workspace/workspacessh/manual_test/%.sh,$(MAKECMDGOALS))

manual-ssh-test: rune-release-linux-$(RUNE_LINUX_HOST_ARCH)
	@if [ -z "$(MANUAL_SSH_SCRIPT)" ]; then \
		echo "usage: make manual-ssh-test internal/workspace/workspacessh/manual_test/<file>.sh" >&2; \
		exit 2; \
	fi
	@RUNE_REMOTE_BIN=$(RUNE_LINUX_REMOTE_BIN) ./$(MANUAL_SSH_SCRIPT)

# Swallow the script path argument so make doesn't try to (re)build
# the .sh file as a target. The actual script is invoked by the
# manual-ssh-test recipe above.
internal/workspace/workspacessh/manual_test/%.sh:
	@:

rune-app-amd64:
	@$(MAKE) -C cmd/rune app-amd64

rune-app-arm64:
	@$(MAKE) -C cmd/rune app-arm64

# Like rune-app-arm64 but bakes the staging API endpoints into the binary.
rune-staging-app-arm64:
	@$(MAKE) -C cmd/rune app-arm64 RUNE_ENV=staging

rune-app-delve:
	@$(MAKE) -C cmd/rune app-delve

rune-dmg:
	@$(MAKE) -C cmd/rune dmg

rune-dmg-amd64:
	@$(MAKE) -C cmd/rune dmg-amd64

rune-dmg-notarize:
	@$(MAKE) -C cmd/rune dmg-notarize

rune-dmg-amd64-notarize:
	@$(MAKE) -C cmd/rune dmg-amd64-notarize

rune-release-all:
	@$(MAKE) -C cmd/rune release-all

runectl: CGO_ENABLED=CGO_ENABLED=1
runectl: $(BIN)/runectl

rune-agent-pkg:
	@$(MAKE) -C cmd/rune-agent pkg

rune-agent-sign:
	@$(MAKE) -C cmd/rune-agent sign

rune-agent-notarize:
	@$(MAKE) -C cmd/rune-agent notarize

rune-agent-prod-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/rune-agent dist

rune-agent-staging-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/rune-agent dist

rune-agent-prod-dist-notarized: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/rune-agent dist-notarized

rune-agent-staging-dist-notarized: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/rune-agent dist-notarized

rune-agent-linux-cross-compile:
	@$(MAKE) -C cmd/rune-agent linux-cross-compile

rune-agent-release-linux-amd64:
	@$(MAKE) -C cmd/rune-agent release-linux-amd64

rune-agent-release-linux-arm64:
	@$(MAKE) -C cmd/rune-agent release-linux-arm64

rune-agent-release-linux-amd64-cross:
	@$(MAKE) -C cmd/rune-agent release-linux-amd64-cross

rune-agent-release-linux-arm64-cross:
	@$(MAKE) -C cmd/rune-agent release-linux-arm64-cross

rune-agent-prod-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-amd64) $(MAKE) -C cmd/rune-agent dist-linux-amd64

rune-agent-staging-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-amd64) $(MAKE) -C cmd/rune-agent dist-linux-amd64

rune-agent-prod-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-arm64) $(MAKE) -C cmd/rune-agent dist-linux-arm64

rune-agent-staging-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-arm64) $(MAKE) -C cmd/rune-agent dist-linux-arm64

rune-agent-prod-dist-linux-amd64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-amd64) $(MAKE) -C cmd/rune-agent dist-linux-amd64-cross

rune-agent-staging-dist-linux-amd64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-amd64) $(MAKE) -C cmd/rune-agent dist-linux-amd64-cross

rune-agent-prod-dist-linux-arm64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-arm64) $(MAKE) -C cmd/rune-agent dist-linux-arm64-cross

rune-agent-staging-dist-linux-arm64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-arm64) $(MAKE) -C cmd/rune-agent dist-linux-arm64-cross

rune-agent-prod-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-amd64) $(MAKE) -C cmd/rune-agent dist-darwin-amd64

rune-agent-staging-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-amd64) $(MAKE) -C cmd/rune-agent dist-darwin-amd64

rune-agent-prod-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-arm64) $(MAKE) -C cmd/rune-agent dist-darwin-arm64

rune-agent-staging-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-arm64) $(MAKE) -C cmd/rune-agent dist-darwin-arm64

fuzzy-search: CGO_ENABLED=CGO_ENABLED=1
fuzzy-search: $(BIN)/extension_fuzzy_search

fuzzy-search-pkg:
	@$(MAKE) -C cmd/extension_fuzzy_search pkg

fuzzy-search-prod-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/extension_fuzzy_search dist

fuzzy-search-staging-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/extension_fuzzy_search dist

fuzzy-search-linux-cross-compile:
	@$(MAKE) -C cmd/extension_fuzzy_search linux-cross-compile

fuzzy-search-release-linux-amd64:
	@$(MAKE) -C cmd/extension_fuzzy_search release-linux-amd64

fuzzy-search-release-linux-arm64:
	@$(MAKE) -C cmd/extension_fuzzy_search release-linux-arm64

fuzzy-search-release-linux-amd64-cross:
	@$(MAKE) -C cmd/extension_fuzzy_search release-linux-amd64-cross

fuzzy-search-release-linux-arm64-cross:
	@$(MAKE) -C cmd/extension_fuzzy_search release-linux-arm64-cross

fuzzy-search-prod-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-amd64

fuzzy-search-staging-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-amd64

fuzzy-search-prod-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-arm64

fuzzy-search-staging-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-arm64

fuzzy-search-prod-dist-linux-amd64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-amd64-cross

fuzzy-search-staging-dist-linux-amd64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-amd64-cross

fuzzy-search-prod-dist-linux-arm64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-arm64-cross

fuzzy-search-staging-dist-linux-arm64-cross: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-linux-arm64-cross

fuzzy-search-prod-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-darwin-amd64

fuzzy-search-staging-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-amd64) $(MAKE) -C cmd/extension_fuzzy_search dist-darwin-amd64

fuzzy-search-prod-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-darwin-arm64

fuzzy-search-staging-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-arm64) $(MAKE) -C cmd/extension_fuzzy_search dist-darwin-arm64

runectl-pkg:
	@$(MAKE) -C cmd/runectl pkg

runectl-sign:
	@$(MAKE) -C cmd/runectl sign

runectl-notarize:
	@$(MAKE) -C cmd/runectl notarize

runectl-prod-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/runectl dist

runectl-staging-dist: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/runectl dist

runectl-prod-dist-notarized: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/runectl dist-notarized

runectl-staging-dist-notarized: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,$(BLUECTL_HOST_OS)-$(BLUECTL_HOST_ARCH)) $(MAKE) -C cmd/runectl dist-notarized

runectl-prod-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-amd64) $(MAKE) -C cmd/runectl dist-linux-amd64

runectl-staging-dist-linux-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-amd64) $(MAKE) -C cmd/runectl dist-linux-amd64

runectl-prod-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,linux-arm64) $(MAKE) -C cmd/runectl dist-linux-arm64

runectl-staging-dist-linux-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,linux-arm64) $(MAKE) -C cmd/runectl dist-linux-arm64

runectl-prod-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-amd64) $(MAKE) -C cmd/runectl dist-darwin-amd64

runectl-staging-dist-darwin-amd64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-amd64) $(MAKE) -C cmd/runectl dist-darwin-amd64

runectl-prod-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,prod,darwin-arm64) $(MAKE) -C cmd/runectl dist-darwin-arm64

runectl-staging-dist-darwin-arm64: clean
	@BLUECTL_CONFIG_DIR=$(call BLUECTL_CONFIG,staging,darwin-arm64) $(MAKE) -C cmd/runectl dist-darwin-arm64

notary-credentials:
	xcrun notarytool store-credentials "$(NOTARY_PROFILE)" --team-id "YYZRWD888J"

# Distribution packages (dist/). Both build inside Docker so no
# Debian/Arch host is required; artifacts land in dist/out/.
#
#   make pkg-deb            .deb for the host arch
#   make pkg-deb-amd64      .deb for linux/amd64
#   make pkg-deb-arm64      .deb for linux/arm64
#   make pkg-arch           Arch package from dist/arch/PKGBUILD
#   make pkg-arch-srcinfo   regenerate .SRCINFO only (fast)
#
# The version is resolved here rather than in the container: the build
# context excludes .git, and a git worktree's .git is a file pointing
# outside the context anyway.
PKG_OUT ?= $(TARGET)/pkg
PKG_GO_VERSION ?= 1.26.6
DEB_BASE_IMAGE ?= debian:bookworm
# archlinux is published for amd64 only. Emulating it is not viable:
# the Go toolchain segfaults under qemu-user, so the full Arch build
# needs an x86_64 builder (a native host or CI).
PKG_ARCH_PLATFORM ?= linux/amd64
PKG_HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
PKG_TAG := $(shell git describe --tags --match 'v*')
PKG_COMMIT := $(shell git rev-parse --short HEAD)

pkg-deb: pkg-deb-$(PKG_HOST_ARCH)

pkg-deb-amd64:
	@$(MAKE) pkg-deb-docker PKG_DEB_ARCH=amd64

pkg-deb-arm64:
	@$(MAKE) pkg-deb-docker PKG_DEB_ARCH=arm64

pkg-deb-docker:
	@mkdir -p $(PKG_OUT)
	@echo "Building .deb for linux/$(PKG_DEB_ARCH) on $(DEB_BASE_IMAGE) ($(PKG_TAG)) ..."
	@docker buildx build --rm \
		-f dist/debian/Dockerfile \
		--platform linux/$(PKG_DEB_ARCH) \
		--build-arg BASE_IMAGE=$(DEB_BASE_IMAGE) \
		--build-arg GO_VERSION=$(PKG_GO_VERSION) \
		--build-arg RUNE_TAG=$(PKG_TAG) \
		--build-arg RUNE_COMMIT=$(PKG_COMMIT) \
		--target artifact \
		--output type=local,dest=$(PKG_OUT) \
		.
	@ls -1 $(PKG_OUT)/*.deb

pkg-arch:
	@mkdir -p $(PKG_OUT)
	@echo "Building Arch package from dist/arch/PKGBUILD ($(PKG_TAG)) ..."
	@docker buildx build --rm \
		-f dist/arch/Dockerfile \
		--platform $(PKG_ARCH_PLATFORM) \
		--build-arg RUNE_TAG=$(PKG_TAG) \
		--build-arg RUNE_COMMIT=$(PKG_COMMIT) \
		--target artifact \
		--output type=local,dest=$(PKG_OUT) \
		.
	@ls -1 $(PKG_OUT)/*.pkg.tar.zst

# Regenerates dist/arch/.SRCINFO in place; the AUR requires it to match
# PKGBUILD on every push.
pkg-arch-srcinfo:
	@docker buildx build --rm \
		-f dist/arch/Dockerfile \
		--platform $(PKG_ARCH_PLATFORM) \
		--target srcinfo-artifact \
		--output type=local,dest=dist/arch \
		.
	@echo "Wrote dist/arch/.SRCINFO"

pkg-clean:
	@rm -rf $(PKG_OUT)
