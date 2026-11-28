#!/bin/bash
# Regression test for the .go-source publish gate (verify-no-go-source.sh).
#
# For every component's dist.sh, this drives the real script against a
# STUB artifact that intentionally contains a .go file and asserts the
# script aborts before reaching the publish step. A fake bluectl/gh
# is placed first on PATH so any attempt to publish is itself a failure:
# the gate must fire first.
#
# Usage: dist-with-src-test.sh <tar|dmg>
#   tar  - build a .tar.gz stub and run it through every dist.sh
#   dmg  - build a .dmg stub (macOS/hdiutil only) and run it through
#          every dist.sh
set -u

kind="${1:-}"
case "$kind" in
    tar|dmg) ;;
    *) echo "usage: dist-with-src-test.sh <tar|dmg>" >&2; exit 2 ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Each dist.sh lives next to its component and resolves
# verify-no-go-source.sh relative to its own BASH_SOURCE, so we invoke
# the scripts by their real in-tree paths.
dist_scripts=(
    "$repo_root/cmd/rune/dist.sh"
    "$repo_root/cmd/rune-agent/dist.sh"
    "$repo_root/cmd/runectl/dist.sh"
    "$repo_root/cmd/extension_fuzzy_search/dist.sh"
)

work="$(mktemp -d "${TMPDIR:-/tmp}/dist-with-src-XXXXXX")"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT

# A fake PATH where bluectl/gh abort loudly. Reaching either means
# the gate did NOT stop the publish, which is the bug we guard against.
fakebin="$work/fakebin"
mkdir -p "$fakebin"
for tool in bluectl gh; do
    cat >"$fakebin/$tool" <<EOF
#!/bin/bash
echo "FAIL: $tool was invoked — the .go-source gate did not abort the publish." >&2
exit 99
EOF
    chmod +x "$fakebin/$tool"
done

# Hermetic git repo tagged with a publishable version so cmd/rune's
# check-release-tag.sh passes and we actually exercise the gate rather
# than tripping the tag guard first.
gitrepo="$work/repo"
mkdir -p "$gitrepo"
(
    cd "$gitrepo"
    git init -q
    git config user.email tester@example.com
    git config user.name tester
    git config commit.gpgSign false
    git remote add origin https://example.com/rune.git
    git commit -q --allow-empty -m "seed"
    git tag v0.0.0
) || { echo "FAIL: could not seed hermetic git repo" >&2; exit 1; }

# Build the stub artifact: a payload tree containing one .go file.
payload="$work/payload"
mkdir -p "$payload/bin"
echo "stub-binary" >"$payload/bin/rune"
echo "package main" >"$payload/leaked.go"

if [[ "$kind" == "tar" ]]; then
    artifact="$work/Rune-v0.0.0.tar.gz"
    tar -czf "$artifact" -C "$payload" .
else
    if ! command -v hdiutil >/dev/null 2>&1; then
        echo "SKIP: hdiutil not available; dmg gate test requires macOS." >&2
        exit 0
    fi
    artifact="$work/Rune-v0.0.0.dmg"
    hdiutil create -quiet -ov -volname "Rune" -srcfolder "$payload" \
        -format UDZO "$artifact" \
        || { echo "FAIL: could not build stub dmg" >&2; exit 1; }
fi

# Dummy env that satisfies each dist.sh's pre-gate validation so the
# gate is the first thing that can stop the run.
export BLUECTL_CONFIG_DIR="$work/bluectl-config"
mkdir -p "$BLUECTL_CONFIG_DIR"
export BLUE_PGP_KEY="test-key"
export BLUE_PGP_KEYRING="$work/keyring.gpg"
: >"$BLUE_PGP_KEYRING"
export BLUE_RELEASE_TAR="$artifact"
export BLUE_TARGET_OS="darwin"
export BLUE_TARGET_ARCH="arm64"
export RELEASE_REPO="example/rune"
export DOWNLOAD_HOST="https://example.com"
export RUNECTL_VERSION="v1.1.3"

failures=0
for script in "${dist_scripts[@]}"; do
    name="$(basename "$(dirname "$script")")"
    echo "=== $name: expect .go-source gate to abort publish ==="
    out="$(cd "$gitrepo" && PATH="$fakebin:$PATH" bash "$script" 2>&1)"
    status=$?

    if [[ $status -eq 0 ]]; then
        echo "$out"
        echo "FAIL [$name]: dist.sh exited 0 — the .go-source gate did not fire." >&2
        failures=$((failures + 1))
        continue
    fi
    if grep -q "was invoked" <<<"$out"; then
        echo "$out"
        echo "FAIL [$name]: publish tool ran before the gate aborted." >&2
        failures=$((failures + 1))
        continue
    fi
    if ! grep -q "contains .go source files" <<<"$out"; then
        echo "$out"
        echo "FAIL [$name]: aborted, but not via the .go-source gate." >&2
        failures=$((failures + 1))
        continue
    fi
    echo "ok [$name]: gate aborted publish (exit $status)"
done

if [[ $failures -ne 0 ]]; then
    echo "FAILED: $failures component(s) did not enforce the .go-source gate." >&2
    exit 1
fi
echo "PASS: every dist.sh aborted the $kind publish on .go source."
